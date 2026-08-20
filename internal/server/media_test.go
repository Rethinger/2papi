package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/resilience"
)

func mediaSnapshot(t *testing.T, upstreamURL string) *config.Snapshot {
	t.Helper()
	snap, err := config.Build(config.Config{
		Version: 1,
		Secret:  "s",
		VirtualKeys: []config.VirtualKey{
			{Name: "vk", Key: "sk", Models: []string{"img-model", "tts-model", "stt-model"}, RPM: 100},
		},
		Models: []config.Model{
			{Alias: "img-model", UpstreamModel: "dall-e-3", Accounts: []string{"acct-0"}},
			{Alias: "tts-model", UpstreamModel: "tts-1", Accounts: []string{"acct-0"}},
			{Alias: "stt-model", UpstreamModel: "whisper-1", Accounts: []string{"acct-0"}},
		},
		Accounts: []config.Account{
			{Name: "acct-0", BaseURL: upstreamURL, APIKey: "k", Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestImagesGenerationsPassthroughWithModelRewrite(t *testing.T) {
	var upstreamBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			http.NotFound(w, r)
			return
		}
		upstreamBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":123,"data":[{"url":"https://cdn/img.png"}]}`)
	}))
	defer up.Close()

	gw := NewRuntimeServer(mediaSnapshot(t, up.URL), resilience.New())
	h := gw.Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		bytes.NewReader([]byte(`{"model":"img-model","prompt":"a cat","n":1,"size":"1024x1024"}`)))
	req.Header.Set("Authorization", "Bearer sk")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("images status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"url":"https://cdn/img.png"`) {
		t.Fatalf("unexpected images body: %s", rec.Body.String())
	}
	if !strings.Contains(string(upstreamBody), `"model":"dall-e-3"`) {
		t.Fatalf("upstream model was not rewritten: %s", string(upstreamBody))
	}
}

func TestAudioSpeechPassthroughBinary(t *testing.T) {
	audioBytes := []byte{0x52, 0x49, 0x46, 0x46, 0x00, 0x01, 0x02, 0x03, 0xff, 0xfe, 0x00, 0x7f}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(audioBytes)
	}))
	defer up.Close()

	gw := NewRuntimeServer(mediaSnapshot(t, up.URL), resilience.New())
	h := gw.Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech",
		bytes.NewReader([]byte(`{"model":"tts-model","input":"hello","voice":"alloy"}`)))
	req.Header.Set("Authorization", "Bearer sk")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("speech status=%d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), audioBytes) {
		t.Fatalf("audio bytes were altered: got %x want %x", rec.Body.Bytes(), audioBytes)
	}
}

func TestModerationsPassthrough(t *testing.T) {
	var upstreamModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/moderations" {
			http.NotFound(w, r)
			return
		}
		var payload struct{ Model string `json:"model"` }
		_ = json.NewDecoder(r.Body).Decode(&payload)
		upstreamModel = payload.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"modr-1","model":"omni-moderation-latest","results":[{"flagged":false}]}`)
	}))
	defer up.Close()

	gw := NewRuntimeServer(mediaSnapshot(t, up.URL), resilience.New())
	h := gw.Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/moderations",
		bytes.NewReader([]byte(`{"model":"stt-model","input":"harmless text"}`)))
	req.Header.Set("Authorization", "Bearer sk")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("moderations status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"flagged":false`) {
		t.Fatalf("unexpected moderations body: %s", rec.Body.String())
	}
	if upstreamModel != "whisper-1" {
		t.Fatalf("model alias was not rewritten to upstream id: %q", upstreamModel)
	}
}

func TestAudioTranscriptionsMultipartModelRewrite(t *testing.T) {
	var upstreamModel string
	var upstreamFields map[string][]string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(64 << 20); err == nil {
			upstreamModel = r.FormValue("model")
			upstreamFields = r.MultipartForm.Value
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"transcribed text"}`)
	}))
	defer up.Close()

	gw := NewRuntimeServer(mediaSnapshot(t, up.URL), resilience.New())
	h := gw.Routes()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("model", "stt-model")
	part, err := writer.CreateFormFile("file", "audio.mp3")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte{0x49, 0x44, 0x33, 0x03, 0x00, 0x00, 0x00})
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Authorization", "Bearer sk")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("transcription status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "transcribed text") {
		t.Fatalf("unexpected transcription body: %s", rec.Body.String())
	}
	if upstreamModel != "whisper-1" {
		t.Fatalf("upstream multipart model=%q, want whisper-1", upstreamModel)
	}
	if len(upstreamFields["model"]) != 1 || upstreamFields["model"][0] != "whisper-1" {
		t.Fatalf("rewritten form values=%v, want model=whisper-1", upstreamFields["model"])
	}
}
