// Package license implements offline validation of signed 2papi licenses.
//
// Format (one line):  <prefix>:<base64url(payloadJSON)>.<base64url(sig)>
//   - prefix is the edition id ("ent" | "cloud");
//   - sig = ed25519.Sign(private, []byte(prefix+"."+b64payload)) — the
//     prefix is inside the signature, so swapping editions breaks it;
//   - payload is canonical JSON (see License struct).
//
// Public key resolution: env 2PAPI_LICENSE_PUBKEY (hex) overrides the
// embedded default. No key configured => every license fails with
// ErrNoKey and the caller degrades to OSS. Validation never touches
// the network — air-gapped deployments work out of the box.
//
// Spec: plan/build-spine-specs.md (шаг 1 хребта).
package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// PubKeyEnv overrides the embedded public key (hex-encoded).
	PubKeyEnv = "2PAPI_LICENSE_PUBKEY"

	// DefaultPubKeyHex is replaced at release time with the real
	// production key. Empty means "no licenses trusted yet".
	DefaultPubKeyHex = ""

	// Edition ids allowed as license prefix/payload edition.
	EdEnt   = "ent"
	EdCloud = "cloud"
)

var (
	ErrBadFormat       = errors.New("license: bad format")
	ErrBadSig          = errors.New("license: bad signature")
	ErrExpired         = errors.New("license: expired")
	ErrNotYet          = errors.New("license: not valid yet")
	ErrNoKey           = errors.New("license: no trusted public key configured")
	ErrUnknownFeature  = errors.New("license: unknown feature")
	ErrEditionMismatch = errors.New("license: edition mismatch")
)

// knownFeatures gates which feature flags a license may carry.
// Unknown flags fail validation so typos can't silently unlock nothing.
var knownFeatures = map[string]bool{
	"sso": true, "orgs": true, "audit_export": true, "secrets": true,
	"ipacl": true, "guardrails": true, "multiregion": true,
	"branding": true, "cc_gateway": true,
}

// License is the decoded payload. JSON field order here is also the
// canonical generation order (lexicographic) for our own keygen.
type License struct {
	Edition    string   `json:"ed"`
	CustomerID string   `json:"cid"`
	CapacityM  int64    `json:"cap"`            // годовая ёмкость запросов, млн; 0 = без лимита
	IssuedAt   int64    `json:"iat"`            // unix sec
	ExpiresAt  int64    `json:"exp"`            // unix sec
	Features   []string `json:"f,omitempty"`    // включённые фичи
	NotBefore  int64    `json:"nbf,omitempty"`  // unix sec
	Trial      bool     `json:"trial,omitempty"`
}

func publicKey() (ed25519.PublicKey, error) {
	hexKey := os.Getenv(PubKeyEnv)
	if hexKey == "" {
		hexKey = DefaultPubKeyHex
	}
	if strings.TrimSpace(hexKey) == "" {
		return nil, ErrNoKey
	}
	raw, err := hex.DecodeString(strings.TrimSpace(hexKey))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: bad %s", ErrNoKey, PubKeyEnv)
	}
	return ed25519.PublicKey(raw), nil
}

// Validate parses and verifies s at time now.
func Validate(s string, now time.Time) (*License, error) {
	s = strings.TrimSpace(s)
	colon := strings.Index(s, ":")
	dot := strings.LastIndex(s, ".")
	if colon <= 0 || dot <= colon+1 || dot == len(s)-1 {
		return nil, ErrBadFormat
	}
	prefix := s[:colon]
	b64payload := s[colon+1 : dot]
	b64sig := s[dot+1:]

	pub, err := publicKey()
	if err != nil {
		return nil, err
	}

	sig, err := base64.RawURLEncoding.DecodeString(b64sig)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadFormat, err)
	}
	msg := []byte(prefix + "." + b64payload)
	if !ed25519.Verify(pub, msg, sig) {
		return nil, ErrBadSig
	}

	rawPayload, err := base64.RawURLEncoding.DecodeString(b64payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadFormat, err)
	}
	var lic License
	if err := json.Unmarshal(rawPayload, &lic); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadFormat, err)
	}
	if lic.Edition != prefix {
		return nil, ErrEditionMismatch
	}
	for _, f := range lic.Features {
		if !knownFeatures[f] {
			return nil, fmt.Errorf("%w: %q", ErrUnknownFeature, f)
		}
	}
	unix := now.Unix()
	if lic.ExpiresAt > 0 && unix >= lic.ExpiresAt {
		return nil, ErrExpired
	}
	if lic.NotBefore > 0 && unix < lic.NotBefore {
		return nil, ErrNotYet
	}
	return &lic, nil
}

// LoadFile validates the license stored in path (trailing newline ok).
func LoadFile(path string, now time.Time) (*License, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Validate(string(data), now)
}
