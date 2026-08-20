package codex

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/config"
)

// generatePath is the ChatGPT backend image generation endpoint used by the
// web app for DALL·E / gpt-image models. It returns an SSE stream whose
// assistant message carries the generated image as an asset pointer.
const generatePath = "/backend-api/generate"

const maxImageDownloadBytes = 16 << 20

// executeImages serves POST /v1/images/generations through a Codex (ChatGPT)
// account. The OpenAI-style request is translated into the backend generate
// payload; the generated asset is downloaded with the account credentials and
// returned to the client as base64 so the upstream URL never leaks.
func (a *Adapter) executeImages(ctx context.Context, ex adapter.Execution) (*adapter.Result, error) {
	if ex.Model.UpstreamModel == "" || ex.PublicModel == "" {
		return nil, fmt.Errorf("invalid model mapping")
	}
	req, err := parseImagesGenerationRequest(ex.Body)
	if err != nil {
		return nil, err
	}
	if req.Prompt == "" {
		return nil, fmt.Errorf("invalid images request: prompt required")
	}
	count := req.N
	if count <= 0 {
		count = 1
	}
	if count > 10 {
		return nil, fmt.Errorf("invalid images request: n must be between 1 and 10")
	}

	cred, _, _, err := a.auth.accessToken(ctx, ex.Account, false)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(a.options.BackendBaseURL, "/")
	pointers, revised, err := a.collectGeneratedAssets(ctx, ex, cred, base, req, count)
	if err != nil {
		// A stale token is the most common cause; refresh once and retry.
		var statusErr generateStatusError
		if errors.As(err, &statusErr) && statusErr.Status == http.StatusUnauthorized {
			cred, _, _, err = a.auth.accessToken(ctx, ex.Account, true)
			if err != nil {
				return nil, err
			}
			pointers, revised, err = a.collectGeneratedAssets(ctx, ex, cred, base, req, count)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	if len(pointers) == 0 {
		return nil, fmt.Errorf("codex image generation returned no image")
	}

	var images []imagesGenerationData
	for _, pointer := range pointers {
		downloadURL, err := imageDownloadURL(base, pointer)
		if err != nil {
			return nil, err
		}
		b, err := a.downloadImage(ctx, cred, downloadURL)
		if err != nil {
			return nil, err
		}
		images = append(images, imagesGenerationData{B64JSON: base64.StdEncoding.EncodeToString(b), RevisedPrompt: revised})
	}
	response, err := json.Marshal(imagesGenerationResponse{Created: time.Now().Unix(), Data: images})
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	header.Set("Content-Length", fmt.Sprint(len(response)))
	return &adapter.Result{Status: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(response))}, nil
}

// collectGeneratedAssets runs one generate call per requested image and
// merges the asset pointers from every stream.
func (a *Adapter) collectGeneratedAssets(ctx context.Context, ex adapter.Execution, cred config.Credential, base string, req imagesGenerationRequest, count int) ([]string, string, error) {
	var pointers []string
	var revised string
	for i := 0; i < count; i++ {
		resp, err := a.generateOnce(ctx, ex, cred, base, req)
		if err != nil {
			return nil, "", err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			defer resp.Body.Close()
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			return nil, "", generateStatusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxImageDownloadBytes+1))
		resp.Body.Close()
		if err != nil {
			return nil, "", err
		}
		if len(raw) > maxImageDownloadBytes {
			return nil, "", fmt.Errorf("codex image stream exceeds limit")
		}
		streamPointers, streamRevised, err := parseGenerateStream(raw)
		if err != nil {
			return nil, "", err
		}
		pointers = append(pointers, streamPointers...)
		if streamRevised != "" {
			revised = streamRevised
		}
	}
	return pointers, revised, nil
}

type generateStatusError struct {
	Status int
	Body   string
}

func (e generateStatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("codex image generation status %d: %s", e.Status, e.Body)
	}
	return fmt.Sprintf("codex image generation status %d", e.Status)
}

// generateOnce posts a single generate payload and returns the upstream
// response (stream for 2xx, error body otherwise).
func (a *Adapter) generateOnce(ctx context.Context, ex adapter.Execution, cred config.Credential, base string, req imagesGenerationRequest) (*http.Response, error) {
	payload := generateRequest{
		Prompt:                     req.Prompt,
		ConversationMode:           map[string]any{"kind": "primary_assistant"},
		ForceParagenModelSlug:      ex.Model.UpstreamModel,
		ParentMessageID:            randomUUID(),
		Messages:                   []any{},
		TimezoneOffsetMin:          0,
		HistoryAndTrainingDisabled: true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	u, err := url.JoinPath(base, generatePath)
	if err != nil {
		return nil, err
	}
	reqHTTP, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	reqHTTP.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	reqHTTP.Header.Set("Content-Type", "application/json")
	reqHTTP.Header.Set("Accept", "text/event-stream")
	reqHTTP.Header.Set("ChatGPT-Account-ID", cred.ChatGPTAccountID)
	reqHTTP.Header.Set("client_version", a.options.ClientVersion)
	reqHTTP.Header.Set("User-Agent", a.options.ClientVersion)
	reqHTTP.Header.Set("X-Codex-Client", a.options.ClientVersion)
	return a.client.Do(reqHTTP)
}

// parseGenerateStream folds the backend generate SSE stream into the asset
// pointers of every generated image plus the last revised prompt. The stream
// is a sequence of "data: {…}" JSON lines without event names.
func parseGenerateStream(raw []byte) ([]string, string, error) {
	var pointers []string
	var revised string
	finished := false
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		data, ok := sseDataPayload(bytes.TrimSuffix(line, []byte{'\r'}))
		if !ok || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			continue
		}
		var event generateEvent
		if json.Unmarshal(data, &event) != nil {
			continue
		}
		if event.Status == "failed" || (event.Error != nil && event.Error.Message != "") {
			message := "codex image generation failed"
			if event.Error != nil && event.Error.Message != "" {
				message = "codex image generation failed: " + event.Error.Message
			}
			return nil, "", fmt.Errorf("%s", message)
		}
		if event.MessageType == "assistant" && event.Content != nil {
			streamPointers, streamTexts := event.Content.pointersAndTexts()
			pointers = append(pointers, streamPointers...)
			for _, text := range streamTexts {
				revised = text
			}
		}
		if event.Status == "finished_successfully" {
			finished = true
		}
	}
	if !finished {
		return nil, "", fmt.Errorf("codex image generation did not finish successfully")
	}
	return pointers, revised, nil
}

type generateEvent struct {
	MessageType string           `json:"message_type"`
	Status      string           `json:"status"`
	Content     *generateContent `json:"content"`
	Error       *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type generateContent struct {
	ContentType string            `json:"content_type"`
	Parts       []json.RawMessage `json:"parts"`
}

// pointersAndTexts folds the assistant content parts into generated image
// asset pointers and text fragments. Parts are either plain strings (text) or
// objects; image parts carry an asset_pointer.
func (c *generateContent) pointersAndTexts() (pointers []string, texts []string) {
	for _, raw := range c.Parts {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			if strings.TrimSpace(text) != "" {
				texts = append(texts, text)
			}
			continue
		}
		var part generatePart
		if json.Unmarshal(raw, &part) != nil {
			continue
		}
		if part.AssetPointer != "" {
			pointers = append(pointers, part.AssetPointer)
		}
		if part.Text != "" {
			texts = append(texts, part.Text)
		}
		for _, nested := range part.Parts {
			if strings.TrimSpace(nested) != "" {
				texts = append(texts, nested)
			}
		}
	}
	return pointers, texts
}

type generatePart struct {
	ContentType  string   `json:"content_type"`
	AssetPointer string   `json:"asset_pointer"`
	Text         string   `json:"text"`
	Parts        []string `json:"parts,omitempty"`
}

// imageDownloadURL resolves a backend asset pointer to a downloadable URL.
// Pointers arrive as "file-service://<id>"; the web app downloads them from
// /backend-api/files/<id>/download.
func imageDownloadURL(base, pointer string) (string, error) {
	trimmed := strings.TrimSpace(pointer)
	if trimmed == "" {
		return "", fmt.Errorf("codex image generation returned an empty asset pointer")
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return trimmed, nil
	}
	if strings.HasPrefix(trimmed, "file-service://") {
		trimmed = strings.TrimPrefix(trimmed, "file-service://")
	}
	return url.JoinPath(base, "/backend-api/files/", trimmed, "download")
}

func (a *Adapter) downloadImage(ctx context.Context, cred config.Credential, downloadURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("ChatGPT-Account-ID", cred.ChatGPTAccountID)
	req.Header.Set("client_version", a.options.ClientVersion)
	req.Header.Set("User-Agent", a.options.ClientVersion)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("codex image download status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxImageDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxImageDownloadBytes {
		return nil, fmt.Errorf("codex image download exceeds limit")
	}
	return b, nil
}

type imagesGenerationRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	Quality        string `json:"quality,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	User           string `json:"user,omitempty"`
}

type generateRequest struct {
	Prompt                     string         `json:"prompt"`
	ConversationMode           map[string]any `json:"conversation_mode"`
	ForceParagenModelSlug      string         `json:"force_paragen_model_slug"`
	ParentMessageID            string         `json:"parent_message_id"`
	Messages                   []any          `json:"messages"`
	TimezoneOffsetMin          int            `json:"timezone_offset_min"`
	HistoryAndTrainingDisabled bool           `json:"history_and_training_disabled"`
}

type imagesGenerationResponse struct {
	Created int64                  `json:"created"`
	Data    []imagesGenerationData `json:"data"`
}

type imagesGenerationData struct {
	B64JSON       string `json:"b64_json"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

func parseImagesGenerationRequest(raw []byte) (imagesGenerationRequest, error) {
	var in imagesGenerationRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&in); err != nil {
		return in, fmt.Errorf("invalid images request: %w", err)
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		return in, fmt.Errorf("invalid images request: trailing tokens")
	}
	return in, nil
}

// randomUUID returns a random v4 UUID without external dependencies.
func randomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16]))
}
