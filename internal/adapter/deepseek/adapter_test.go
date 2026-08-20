package deepseek

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/config"
)

func testExec(url string, body string) adapter.Execution {
	return adapter.Execution{
		Endpoint:    adapter.EndpointChatCompletions,
		Account:     config.Account{Name: "ds", BaseURL: url, Credential: config.Credential{Kind: "api_key", APIKey: "sk-ds"}},
		Model:       config.Model{Alias: "ds-pro", UpstreamModel: "deepseek-v4-pro-0813"},
		PublicModel: "ds-pro",
		Body:        []byte(body),
	}
}

func TestNonStreamingRewritesModel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "no ua", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"deepseek-v4-pro-0813","choices":[{"message":{"content":"ok","reasoning_content":"think"}}]}`)
	}))
	defer ts.Close()

	ad := New(ts.Client())
	res, err := ad.Execute(context.Background(), testExec(ts.URL, `{"model":"ds-pro","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), `"model":"ds-pro"`) {
		t.Fatalf("model not rewritten: %s", b)
	}
	if !strings.Contains(string(b), "think") {
		t.Fatalf("reasoning_content dropped: %s", b)
	}
}

func TestStreamingSSEPassthroughWithModel(t *testing.T) {
	events := []string{
		`data: {"id":"x","model":"deepseek-v4-pro-0813","choices":[{"delta":{"reasoning_content":"thinking"}}]}`,
		`data: {"id":"x","model":"deepseek-v4-pro-0813","choices":[{"delta":{"content":"answer"}}]}`,
		`data: [DONE]`,
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, e := range events {
			_, _ = io.WriteString(w, e+"\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer ts.Close()

	ad := New(ts.Client())
	res, err := ad.Execute(context.Background(), testExec(ts.URL, `{"model":"ds-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	var all strings.Builder
	for sc.Scan() {
		all.WriteString(sc.Text())
		all.WriteByte('\n')
	}
	out := all.String()
	if !strings.Contains(out, `"model":"ds-pro"`) {
		t.Fatalf("model not rewritten in SSE: %s", out)
	}
	if !strings.Contains(out, "reasoning_content") || !strings.Contains(out, `thinking`) {
		t.Fatalf("reasoning_content passthrough lost: %s", out)
	}
	if !strings.Contains(out, `content":"answer`) {
		t.Fatalf("content missing: %s", out)
	}
}

func TestJoinURL(t *testing.T) {
	cases := [][2]string{
		{"https://api.deepseek.com", "https://api.deepseek.com/chat/completions"},
		{"https://api.deepseek.com/v1", "https://api.deepseek.com/v1/chat/completions"},
	}
	for _, c := range cases {
		got, err := joinURL(c[0], ChatPath)
		if err != nil {
			t.Fatal(err)
		}
		if got != c[1] {
			t.Fatalf("join(%s)=%s want %s", c[0], got, c[1])
		}
	}
}
