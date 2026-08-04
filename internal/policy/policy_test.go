package policy

import (
	"net/http/httptest"
	"testing"

	"github.com/1jehuang/2papi/internal/config"
)

func testSnapshot(t *testing.T) *config.Snapshot {
	t.Helper()
	s, err := config.Build(config.Config{
		Version: 1,
		Secret:  "secret",
		VirtualKeys: []config.VirtualKey{
			{Name: "limited", Key: "sk-limited", Models: []string{"model-a"}, RPM: 1},
			{Name: "all", Key: "sk-all", Models: []string{"*"}},
		},
		Models:   []config.Model{{Alias: "model-a", UpstreamModel: "upstream", Accounts: []string{"account"}}},
		Accounts: []config.Account{{Name: "account", BaseURL: "http://example.test", APIKey: "upstream-key", Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuthenticateAndModelPermissions(t *testing.T) {
	auth := New(testSnapshot(t))

	missing := httptest.NewRequest("GET", "/", nil)
	if _, ok := auth.Authenticate(missing); ok {
		t.Fatal("missing bearer token authenticated")
	}

	wrong := httptest.NewRequest("GET", "/", nil)
	wrong.Header.Set("Authorization", "Bearer wrong")
	if _, ok := auth.Authenticate(wrong); ok {
		t.Fatal("wrong bearer token authenticated")
	}

	valid := httptest.NewRequest("GET", "/", nil)
	valid.Header.Set("Authorization", "Bearer sk-limited")
	vk, ok := auth.Authenticate(valid)
	if !ok || vk.Name != "limited" {
		t.Fatalf("valid token rejected: ok=%v key=%+v", ok, vk)
	}
	if !Allows(vk, "model-a") || Allows(vk, "model-b") {
		t.Fatal("model allowlist was not enforced")
	}

	wildcard := config.VirtualKey{Models: []string{"*"}}
	if !Allows(wildcard, "anything") || !Allows(config.VirtualKey{}, "anything") {
		t.Fatal("wildcard or empty model allowlist should allow access")
	}
}

func TestRateLimitBucket(t *testing.T) {
	auth := New(testSnapshot(t))
	vk := config.VirtualKey{Name: "limited", RPM: 1}
	if !auth.AllowRate(vk) {
		t.Fatal("first request should consume initial token")
	}
	if auth.AllowRate(vk) {
		t.Fatal("second immediate request should be rate limited")
	}
	if !auth.AllowRate(config.VirtualKey{Name: "unlimited", RPM: 0}) {
		t.Fatal("non-positive RPM should be unlimited")
	}
}
