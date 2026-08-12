package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/1jehuang/2papi/internal/adapter"
	"github.com/1jehuang/2papi/internal/config"
)

const responsesPath = "/backend-api/codex/responses"
const maxRateLimitObservationBytes = 64 << 10

func (a *Adapter) executeResponses(ctx context.Context, ex adapter.Execution) (*adapter.Result, error) {
	if ex.Model.UpstreamModel == "" || ex.PublicModel == "" {
		return nil, fmt.Errorf("invalid model mapping")
	}
	clientWantsStream := isStreamingRequest(ex.Body)
	body, err := rewriteResponsesRequestModel(ex.Body, ex.Model.UpstreamModel)
	if err != nil {
		return nil, err
	}
	cred, _, _, err := a.auth.accessToken(ctx, ex.Account, false)
	if err != nil {
		return nil, err
	}
	u, err := url.JoinPath(strings.TrimRight(a.options.BackendBaseURL, "/"), responsesPath)
	if err != nil {
		return nil, err
	}
	resp, err := a.doResponsesRequest(ctx, ex, u, body, cred, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		cred, _, _, err = a.auth.accessToken(ctx, ex.Account, true)
		if err != nil {
			return nil, err
		}
		resp, err = a.doResponsesRequest(ctx, ex, u, body, cred, true)
	}
	if err != nil {
		return nil, err
	}
	a.observeRateLimits(ctx, ex, resp.Header, nil)
	out := resp.Body
	if clientWantsStream {
		out = rewriteResponsesSSE(resp.Body, ex.Model.UpstreamModel, ex.PublicModel, func(raw json.RawMessage) {
			a.observeRateLimits(context.Background(), ex, nil, raw)
		})
	} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		b, rawRateLimits, err := collectResponsesSSE(resp.Body, ex.Model.UpstreamModel, ex.PublicModel, 16<<20)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		a.observeRateLimits(ctx, ex, nil, rawRateLimits)
		out = io.NopCloser(bytes.NewReader(b))
		resp.Header.Set("Content-Type", "application/json")
		resp.Header.Set("Content-Length", strconv.Itoa(len(b)))
	} else {
		b, rawRateLimits, err := rewriteJSONModelAndRateLimits(resp.Body, ex.Model.UpstreamModel, ex.PublicModel, 16<<20)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		a.observeRateLimits(ctx, ex, nil, rawRateLimits)
		out = io.NopCloser(bytes.NewReader(b))
		resp.Header.Set("Content-Length", strconv.Itoa(len(b)))
	}
	return &adapter.Result{Status: resp.StatusCode, Header: safeResponseHeaders(resp.Header), Body: out}, nil
}

func (a *Adapter) doResponsesRequest(ctx context.Context, ex adapter.Execution, u string, body []byte, cred config.Credential, upstreamStream bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copySafeRequestHeaders(req.Header, ex.Request.Header)
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", acceptHeader(ex.Request.Header.Get("Accept"), upstreamStream))
	req.Header.Set("ChatGPT-Account-ID", cred.ChatGPTAccountID)
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("User-Agent", a.options.ClientVersion)
	req.Header.Set("X-Codex-Client", a.options.ClientVersion)
	return a.client.Do(req)
}

func (a *Adapter) observeRateLimits(ctx context.Context, ex adapter.Execution, h http.Header, raw json.RawMessage) {
	if a.options.CodexRateLimitObserver == nil {
		return
	}
	safeHeaders := safeRateLimitObservationHeaders(h)
	safeJSON := boundedRateLimitJSON(raw)
	if len(safeHeaders) == 0 && len(safeJSON) == 0 {
		return
	}
	a.options.CodexRateLimitObserver(ctx, CodexRateLimitObservation{Account: ex.Account.Name, PublicModel: ex.PublicModel, Header: safeHeaders, JSON: safeJSON})
}

func boundedRateLimitJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || len(raw) > maxRateLimitObservationBytes {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func safeRateLimitObservationHeaders(h http.Header) http.Header {
	out := http.Header{}
	for k, vals := range h {
		if !isRateLimitObservationHeader(k) {
			continue
		}
		for _, v := range vals {
			if len(v) > 256 {
				v = v[:256]
			}
			out.Add(k, v)
		}
	}
	return out
}

func isRateLimitObservationHeader(k string) bool {
	lk := strings.ToLower(k)
	if lk == "retry-after" || lk == "x-codex-promo-message" || lk == "x-codex-rate-limit-reached-type" {
		return true
	}
	if strings.HasPrefix(lk, "x-codex-credits-") {
		return true
	}
	return strings.HasPrefix(lk, "x-") && (strings.HasSuffix(lk, "-primary-used-percent") || strings.HasSuffix(lk, "-primary-window-minutes") || strings.HasSuffix(lk, "-primary-reset-at") || strings.HasSuffix(lk, "-secondary-used-percent") || strings.HasSuffix(lk, "-secondary-window-minutes") || strings.HasSuffix(lk, "-secondary-reset-at"))
}

func rewriteResponsesRequestModel(b []byte, upstream string) ([]byte, error) {
	var m map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid json: trailing tokens")
	}
	if _, ok := m["model"]; !ok {
		return nil, fmt.Errorf("invalid json: model required")
	}
	if input, ok := m["input"]; ok {
		var text string
		if json.Unmarshal(input, &text) == nil {
			message := []map[string]any{{
				"type":    "message",
				"role":    "user",
				"content": []map[string]string{{"type": "input_text", "text": text}},
			}}
			m["input"], _ = json.Marshal(message)
		}
	}
	m["store"] = json.RawMessage("false")
	m["stream"] = json.RawMessage("true")
	raw, _ := json.Marshal(upstream)
	m["model"] = raw
	return json.Marshal(m)
}

func isStreamingRequest(body []byte) bool {
	var p struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &p) == nil && p.Stream
}

func copySafeRequestHeaders(dst, src http.Header) {
	for k, vals := range src {
		lk := strings.ToLower(k)
		if isUnsafeCodexRequestHeader(lk) || isHopByHopHeader(lk) || namedByConnectionHeader(k, src) {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func isUnsafeCodexRequestHeader(lk string) bool {
	if lk == "authorization" || lk == "host" || lk == "content-length" || lk == "cookie" || lk == "set-cookie" || lk == "x-api-key" || lk == "api-key" {
		return true
	}
	return strings.HasPrefix(lk, "x-gateway-") || strings.Contains(lk, "credential") || strings.Contains(lk, "secret") || strings.Contains(lk, "token") || strings.Contains(lk, "api-key")
}

func acceptHeader(v string, stream bool) string {
	if stream {
		return "text/event-stream"
	}
	if strings.TrimSpace(v) != "" {
		return v
	}
	return "application/json"
}

func safeResponseHeaders(h http.Header) http.Header {
	out := http.Header{}
	for k, vals := range h {
		lk := strings.ToLower(k)
		if lk == "authorization" || lk == "set-cookie" || strings.HasPrefix(lk, "x-gateway-") || isHopByHopHeader(lk) || namedByConnectionHeader(k, h) {
			continue
		}
		for _, v := range vals {
			out.Add(k, v)
		}
	}
	return out
}

func namedByConnectionHeader(k string, h http.Header) bool {
	for _, token := range h.Values("Connection") {
		for _, part := range strings.Split(token, ",") {
			if strings.EqualFold(strings.TrimSpace(part), k) {
				return true
			}
		}
	}
	return false
}

func isHopByHopHeader(lower string) bool {
	switch lower {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	}
	return false
}

func rewriteJSONModelAndRateLimits(r io.Reader, upstream, public string, limit int64) ([]byte, json.RawMessage, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(b)) > limit {
		return nil, nil, fmt.Errorf("upstream response body exceeds limit")
	}
	return rewriteJSONModelBytes(b, upstream, public)
}

func rewriteJSONModelBytes(b []byte, upstream, public string) ([]byte, json.RawMessage, error) {
	var payload map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return b, nil, nil
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return b, nil, nil
	}
	if raw, ok := payload["model"]; ok {
		var cur string
		if json.Unmarshal(raw, &cur) == nil && cur != "" {
			repl, _ := json.Marshal(public)
			payload["model"] = repl
		}
	}
	if raw, ok := payload["response"]; ok {
		if rewritten, changed := rewriteNestedResponseModel(raw, upstream, public); changed {
			payload["response"] = rewritten
		}
	}
	var rateLimits json.RawMessage
	if raw, ok := payload["codex.rate_limits"]; ok && json.Valid(raw) {
		rateLimits = append(json.RawMessage(nil), raw...)
		payload["codex.rate_limits"] = raw
	}
	if typ, ok := payload["type"]; ok {
		var s string
		if json.Unmarshal(typ, &s) == nil && s == "codex.rate_limits" {
			rateLimits = append(json.RawMessage(nil), b...)
		}
	}
	out, err := json.Marshal(payload)
	return out, rateLimits, err
}

func rewriteNestedResponseModel(raw json.RawMessage, upstream, public string) (json.RawMessage, bool) {
	var response map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&response); err != nil {
		return nil, false
	}
	modelRaw, ok := response["model"]
	if !ok {
		return nil, false
	}
	var cur string
	if json.Unmarshal(modelRaw, &cur) != nil || cur == "" {
		return nil, false
	}
	repl, _ := json.Marshal(public)
	response["model"] = repl
	out, err := json.Marshal(response)
	return out, err == nil
}

func collectResponsesSSE(r io.Reader, upstream, public string, limit int64) ([]byte, json.RawMessage, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(b)) > limit {
		return nil, nil, fmt.Errorf("upstream response body exceeds limit")
	}
	trimmed := bytes.TrimSpace(b)
	if !bytes.HasPrefix(trimmed, []byte("data:")) && !bytes.HasPrefix(trimmed, []byte("event:")) {
		return rewriteJSONModelBytes(b, upstream, public)
	}
	var final json.RawMessage
	var rateLimits json.RawMessage
	outputItems := map[int]json.RawMessage{}
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		data, ok := sseDataPayload(bytes.TrimSuffix(line, []byte{'\r'}))
		if !ok || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			continue
		}
		var event map[string]json.RawMessage
		if json.Unmarshal(data, &event) != nil {
			continue
		}
		var typ string
		_ = json.Unmarshal(event["type"], &typ)
		if typ == "codex.rate_limits" {
			rateLimits = append(json.RawMessage(nil), data...)
		}
		if typ == "response.output_item.done" {
			var outputIndex int
			if item, ok := event["item"]; ok && json.Valid(item) && json.Unmarshal(event["output_index"], &outputIndex) == nil {
				outputItems[outputIndex] = append(json.RawMessage(nil), item...)
			}
		}
		if typ == "response.completed" || typ == "response.failed" || typ == "response.incomplete" {
			if response, ok := event["response"]; ok && json.Valid(response) {
				final = append(json.RawMessage(nil), response...)
			}
		}
	}
	if len(final) == 0 {
		return nil, rateLimits, fmt.Errorf("codex stream did not contain a final response")
	}
	if len(outputItems) > 0 {
		var response map[string]json.RawMessage
		if json.Unmarshal(final, &response) == nil {
			ordered := make([]json.RawMessage, 0, len(outputItems))
			for index := 0; len(ordered) < len(outputItems); index++ {
				if item, ok := outputItems[index]; ok {
					ordered = append(ordered, item)
				}
			}
			if output, err := json.Marshal(ordered); err == nil {
				response["output"] = output
				if assembled, err := json.Marshal(response); err == nil {
					final = assembled
				}
			}
		}
	}
	if rewritten, changed := rewriteNestedResponseModel(final, upstream, public); changed {
		final = rewritten
	}
	return final, rateLimits, nil
}

func rewriteResponsesSSE(r io.ReadCloser, upstream, public string, observe func(json.RawMessage)) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer r.Close()
		defer pw.Close()
		s := bufio.NewScanner(r)
		s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for s.Scan() {
			line := s.Bytes()
			if data, ok := sseDataPayload(line); ok {
				if !bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
					if rb, rawRateLimits, err := rewriteJSONModelBytes(data, upstream, public); err == nil {
						if len(rawRateLimits) > 0 && observe != nil {
							observe(rawRateLimits)
						}
						line = append([]byte("data: "), rb...)
					}
				}
			}
			if _, err := pw.Write(append(append([]byte{}, line...), '\n')); err != nil {
				return
			}
		}
		if err := s.Err(); err != nil {
			_ = pw.CloseWithError(err)
		}
	}()
	return pr
}

func sseDataPayload(line []byte) ([]byte, bool) {
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil, false
	}
	data := line[len("data:"):]
	if len(data) > 0 && data[0] == ' ' {
		data = data[1:]
	}
	return data, true
}
