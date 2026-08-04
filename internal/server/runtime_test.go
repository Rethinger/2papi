package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/resilience"
)

func testSnapshot(alias string) *config.Snapshot {
	s, err := config.Build(config.Config{Version: 1, Secret: "s", VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "secret", Models: []string{alias}, RPM: 100000}}, Models: []config.Model{{Alias: alias, UpstreamModel: "u", Accounts: []string{"a"}}}, Accounts: []config.Account{{Name: "a", BaseURL: "http://upstream", APIKey: "ak", Enabled: true}}})
	if err != nil {
		panic(err)
	}
	return s
}

func TestAtomicRuntimeSwapModelsConcurrent(t *testing.T) {
	gw := NewRuntimeServer(testSnapshot("m0"), resilience.New())
	h := gw.Routes()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			gw.Adopt(testSnapshot("m" + string(rune('a'+(i%26)))))
		}(i)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("status=%d", rec.Code)
				return
			}
			var body struct {
				Data []map[string]any `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Errorf("bad json: %v", err)
				return
			}
			if len(body.Data) != 1 {
				t.Errorf("models len=%d", len(body.Data))
			}
		}()
	}
	wg.Wait()
}
