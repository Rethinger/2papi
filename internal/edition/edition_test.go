package edition

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Rethinger/2papi/internal/license"
)

func withEnv(t *testing.T, val string) {
	t.Helper()
	t.Setenv(EnvVar, val)
}

func TestActiveDefaultsToOSS(t *testing.T) {
	withEnv(t, "")
	dir := t.TempDir()
	chdir(t, dir)
	if got := Active(); got != OSS {
		t.Fatalf("no env, no license: want %q got %q", OSS, got)
	}
}

func TestActiveEnvWinsAndUnknownFallsBackToOSS(t *testing.T) {
	withEnv(t, "cloud")
	if !IsCloud() {
		t.Fatal("env cloud should activate cloud")
	}
	withEnv(t, "ENT")
	if !IsEnterprise() {
		t.Fatal("env ENT should activate enterprise")
	}
	withEnv(t, "garbage")
	if !IsOSS() {
		t.Fatal("unknown env must degrade to OSS")
	}
}

// signed writes a properly Ed25519-signed license file and returns it.
func signed(t *testing.T, priv ed25519.PrivateKey, ed string, days int) string {
	t.Helper()
	now := time.Now()
	payload := map[string]any{
		"ed": ed, "cid": "acme", "cap": 1000,
		"iat": now.Unix(), "exp": now.Unix() + int64(days)*86400,
		"f": []string{"sso", "orgs"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	b64p := base64.RawURLEncoding.EncodeToString(raw)
	sig := ed25519.Sign(priv, []byte(ed+"."+b64p))
	return ed + ":" + b64p + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestActiveSignedLicenseFile(t *testing.T) {
	withEnv(t, "") // license file path decides
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(license.PubKeyEnv, hexEncode(pub))

	dir := t.TempDir()
	chdir(t, dir)

	write := func(content string) {
		if err := os.WriteFile(LicenseFile, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(signed(t, priv, "cloud", 30))
	if got := Active(); got != Cloud {
		t.Fatalf("signed cloud license: want %q got %q", Cloud, got)
	}

	write(signed(t, priv, "ent", 365))
	if got := Active(); got != ENT {
		t.Fatalf("signed ent license: want %q got %q", ENT, got)
	}

	// Expired license must degrade to OSS.
	expired := signed(t, priv, "cloud", -1)
	if err := os.WriteFile(LicenseFile, []byte(expired), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Active(); got != OSS {
		t.Fatalf("expired license: want %q got %q", OSS, got)
	}

	// Garbage file must NOT unlock anything.
	write("free tokens!!")
	if got := Active(); got != OSS {
		t.Fatalf("garbage license: want %q got %q", OSS, got)
	}

	// A license signed by a DIFFERENT key must be rejected.
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	write(signed(t, otherPriv, "cloud", 30))
	if got := Active(); got != OSS {
		t.Fatalf("foreign-key license: want %q got %q", OSS, got)
	}

	// No pubkey configured at all: even a valid signature is untrusted.
	os.Unsetenv(license.PubKeyEnv)
	defer os.Setenv(license.PubKeyEnv, hexEncode(pub))
	write(signed(t, priv, "cloud", 30))
	if got := Active(); got != OSS {
		t.Fatalf("no trusted key: want %q got %q", OSS, got)
	}
}

func hexEncode(k ed25519.PublicKey) string {
	return hex.EncodeToString(k)
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}
