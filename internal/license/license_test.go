package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	testPub  ed25519.PublicKey
	testPriv ed25519.PrivateKey
)

func TestMain(m *testing.M) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	testPub, testPriv = pub, priv
	os.Setenv(PubKeyEnv, hexEncode(pub))
	os.Exit(m.Run())
}

func hexEncode(k ed25519.PublicKey) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(k)*2)
	for i, b := range k {
		out[i*2] = hexDigits[b>>4]
		out[i*2+1] = hexDigits[b&0x0f]
	}
	return string(out)
}

func sign(prefix, payloadJSON string) string {
	b64p := base64.RawURLEncoding.EncodeToString([]byte(payloadJSON))
	sig := ed25519.Sign(testPriv, []byte(prefix+"."+b64p))
	return prefix + ":" + b64p + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func mkPayload(ed string, iat, exp int64) string {
	b, _ := json.Marshal(License{
		Edition: ed, CustomerID: "acme", CapacityM: 1000,
		IssuedAt: iat, ExpiresAt: exp,
		Features: []string{"sso", "orgs"},
	})
	return string(b)
}

const day = int64(24 * time.Hour / time.Second)

func TestValidEnterprise(t *testing.T) {
	now := time.Now()
	s := sign("ent", mkPayload("ent", now.Unix()-day, now.Unix()+30*day))
	lic, err := Validate(s, now)
	if err != nil {
		t.Fatalf("valid ent license rejected: %v", err)
	}
	if lic.Edition != "ent" || lic.CustomerID != "acme" || lic.CapacityM != 1000 {
		t.Fatalf("wrong payload: %+v", lic)
	}
}

func TestValidCloud(t *testing.T) {
	now := time.Now()
	lic, err := Validate(sign("cloud", mkPayload("cloud", now.Unix()-day, now.Unix()+day)), now)
	if err != nil || lic.Edition != "cloud" {
		t.Fatalf("valid cloud rejected: %v / %+v", err, lic)
	}
}

func TestTamperedPayloadRejected(t *testing.T) {
	now := time.Now()
	s := sign("ent", mkPayload("ent", now.Unix()-day, now.Unix()+30*day))
	idx := strings.Index(s, ".")
	tampered := s[:idx+1] + flipChar(s[idx+1:])
	if _, err := Validate(tampered, now); err == nil {
		t.Fatal("tampered signature accepted")
	}
}

func TestPrefixSwapRejected(t *testing.T) {
	// Sign an ent license, then present it with a cloud prefix:
	// the signature covers the prefix, so this must fail even though
	// payload.ed could be rewritten too — here we keep ent payload
	// under cloud prefix to prove the prefix is inside the signature.
	now := time.Now()
	payload := mkPayload("ent", now.Unix()-day, now.Unix()+30*day)
	b64p := base64.RawURLEncoding.EncodeToString([]byte(payload))
	sig := ed25519.Sign(testPriv, []byte("ent."+b64p))
	swapped := "cloud:" + b64p + "." + base64.RawURLEncoding.EncodeToString(sig)
	if _, err := Validate(swapped, now); err == nil {
		t.Fatal("prefix swap accepted")
	}
}

func TestExpiredAndNotYet(t *testing.T) {
	now := time.Now()
	expired := sign("ent", mkPayload("ent", now.Unix()-2*day, now.Unix()-day))
	if _, err := Validate(expired, now); !errors.Is(err, ErrExpired) {
		t.Fatalf("want ErrExpired got %v", err)
	}
	futurePayload, _ := json.Marshal(License{
		Edition: "ent", CustomerID: "acme",
		IssuedAt:  now.Unix() + day,
		ExpiresAt: now.Unix() + 2*day,
		NotBefore: now.Unix() + day,
	})
	notYet := sign("ent", string(futurePayload))
	if _, err := Validate(notYet, now); !errors.Is(err, ErrNotYet) {
		t.Fatalf("want ErrNotYet got %v", err)
	}
}

func TestUnknownFeatureRejected(t *testing.T) {
	now := time.Now()
	payload := `{"ed":"ent","cid":"x","cap":1,"iat":` +
		it(now.Unix()-day) + `,"exp":` + it(now.Unix()+day) +
		`,"f":["sso","time_travel"]}`
	if _, err := Validate(sign("ent", payload), now); err == nil {
		t.Fatal("unknown feature accepted")
	}
}

func TestNoKeyConfiguredRejectsEverything(t *testing.T) {
	now := time.Now()
	os.Setenv(PubKeyEnv, "")
	defer os.Setenv(PubKeyEnv, hexEncode(testPub))
	s := sign("ent", mkPayload("ent", now.Unix()-day, now.Unix()+day))
	if _, err := Validate(s, now); err != ErrNoKey {
		t.Fatalf("want ErrNoKey got %v", err)
	}
}

func TestLoadFile(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	p := filepath.Join(dir, "2papi.license")
	if err := os.WriteFile(p, []byte(sign("ent", mkPayload("ent", now.Unix()-day, now.Unix()+day))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lic, err := LoadFile(p, now)
	if err != nil || lic.Edition != "ent" {
		t.Fatalf("LoadFile failed: %v / %+v", err, lic)
	}
}

func flipChar(s string) string {
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}

func it(n int64) string {
	return strings.TrimSpace(strings.Replace(
		strings.Replace(int64ToStr(n), "+", "", 1), " ", "", -1))
}

func int64ToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
