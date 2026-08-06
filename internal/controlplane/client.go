package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/1jehuang/2papi/internal/config"
)

type Client struct {
	BaseURL   string
	Token     string
	GatewayID string
	HTTP      *http.Client
}

type SnapshotIdentity struct {
	ConfigVersion    int64
	SchemaVersion    int
	ConfigChecksum   string
	CredentialDigest string
	RuntimeChecksum  string
	EnvelopeVersion  int
}

func (i SnapshotIdentity) Equal(other SnapshotIdentity) bool {
	return i.ConfigVersion == other.ConfigVersion &&
		i.SchemaVersion == other.SchemaVersion &&
		i.ConfigChecksum == other.ConfigChecksum &&
		i.CredentialDigest == other.CredentialDigest &&
		i.RuntimeChecksum == other.RuntimeChecksum &&
		i.EnvelopeVersion == other.EnvelopeVersion
}

type SnapshotEnvelope struct {
	Version          int             `json:"version"`
	Checksum         string          `json:"checksum"`
	ConfigVersion    *int64          `json:"config_version"`
	SchemaVersion    *int            `json:"schema_version"`
	ConfigChecksum   string          `json:"config_checksum"`
	CredentialDigest string          `json:"credential_digest"`
	RuntimeChecksum  string          `json:"runtime_checksum"`
	Snapshot         json.RawMessage `json:"snapshot"`
	Config           json.RawMessage `json:"config"`
}

type Ack struct {
	GatewayID        string `json:"gateway_id"`
	Version          int    `json:"version,omitempty"`
	Checksum         string `json:"checksum,omitempty"`
	ConfigVersion    int64  `json:"config_version,omitempty"`
	SchemaVersion    int    `json:"schema_version,omitempty"`
	ConfigChecksum   string `json:"config_checksum,omitempty"`
	CredentialDigest string `json:"credential_digest,omitempty"`
	RuntimeChecksum  string `json:"runtime_checksum,omitempty"`
	EnvelopeVersion  int    `json:"envelope_version,omitempty"`
	Success          bool   `json:"success"`
	Error            string `json:"error,omitempty"`
}

func AckForIdentity(id SnapshotIdentity, success bool, errText string) Ack {
	return Ack{Version: int(id.ConfigVersion), Checksum: id.ConfigChecksum, ConfigVersion: id.ConfigVersion, SchemaVersion: id.SchemaVersion, ConfigChecksum: id.ConfigChecksum, CredentialDigest: id.CredentialDigest, RuntimeChecksum: id.RuntimeChecksum, EnvelopeVersion: id.EnvelopeVersion, Success: success, Error: errText}
}

func Enabled(baseURL, token string) bool {
	return strings.TrimSpace(baseURL) != "" && strings.TrimSpace(token) != ""
}

func New(baseURL, token, gatewayID string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, GatewayID: gatewayID, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) Fetch(ctx context.Context) (*config.Snapshot, SnapshotIdentity, error) {
	var zero SnapshotIdentity
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/internal/v1/snapshot", nil)
	if err != nil {
		return nil, zero, err
	}
	c.authorize(req)
	c.capabilities(req, []int{1, 2}, 2)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, zero, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, zero, err
	}
	if resp.StatusCode == http.StatusUpgradeRequired {
		return nil, zero, fmt.Errorf("snapshot fetch upgrade required: %s", strings.TrimSpace(string(body)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, zero, fmt.Errorf("snapshot fetch status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var env SnapshotEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, zero, err
	}
	raw := env.Snapshot
	if len(raw) == 0 {
		raw = env.Config
	}
	if len(raw) == 0 {
		return nil, zero, fmt.Errorf("snapshot payload missing")
	}
	id := identityFromEnvelope(env)
	if isV2Envelope(env) && (env.ConfigVersion == nil || env.SchemaVersion == nil || id.ConfigChecksum == "" || id.CredentialDigest == "" || id.RuntimeChecksum == "") {
		return nil, id, fmt.Errorf("v2 snapshot identity fields required")
	}
	if id.ConfigChecksum == "" {
		return nil, id, fmt.Errorf("snapshot checksum required")
	}
	if id.EnvelopeVersion == 2 {
		if !checksumMatches(raw, id.RuntimeChecksum) {
			return nil, id, fmt.Errorf("runtime checksum mismatch")
		}
	} else if !checksumMatches(raw, id.ConfigChecksum) {
		return nil, id, fmt.Errorf("snapshot checksum mismatch")
	}
	var cfg config.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, id, err
	}
	snap, err := config.Build(cfg)
	if err != nil {
		return nil, id, err
	}
	return snap, id, nil
}

func isV2Envelope(env SnapshotEnvelope) bool {
	return env.ConfigVersion != nil || env.SchemaVersion != nil || env.ConfigChecksum != "" || env.CredentialDigest != "" || env.RuntimeChecksum != ""
}

func identityFromEnvelope(env SnapshotEnvelope) SnapshotIdentity {
	if isV2Envelope(env) {
		id := SnapshotIdentity{ConfigChecksum: env.ConfigChecksum, CredentialDigest: env.CredentialDigest, RuntimeChecksum: env.RuntimeChecksum, EnvelopeVersion: 2}
		if env.ConfigVersion != nil {
			id.ConfigVersion = *env.ConfigVersion
		}
		if env.SchemaVersion != nil {
			id.SchemaVersion = *env.SchemaVersion
		}
		return id
	}
	return SnapshotIdentity{ConfigVersion: int64(env.Version), SchemaVersion: 1, ConfigChecksum: env.Checksum, RuntimeChecksum: env.Checksum, EnvelopeVersion: 1}
}

func (c *Client) Ack(ctx context.Context, ack Ack) error {
	if ack.GatewayID == "" {
		ack.GatewayID = c.GatewayID
	}
	b, _ := json.Marshal(ack)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/internal/v1/gateway-acks", bytes.NewReader(b))
	if err != nil {
		return err
	}
	c.authorize(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ack status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) Heartbeat(ctx context.Context, schemas []int, envelopeVersion int) error {
	body := struct {
		GatewayID        string `json:"gateway_id"`
		SupportedSchemas []int  `json:"supported_schemas"`
		EnvelopeVersion  int    `json:"envelope_version"`
	}{GatewayID: c.GatewayID, SupportedSchemas: schemas, EnvelopeVersion: envelopeVersion}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/internal/v1/gateway-heartbeats", bytes.NewReader(b))
	if err != nil {
		return err
	}
	c.authorize(req)
	c.capabilities(req, schemas, envelopeVersion)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("heartbeat status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Internal-Service-Token", c.Token)
	if c.GatewayID != "" {
		req.Header.Set("X-Gateway-ID", c.GatewayID)
	}
}

func (c *Client) capabilities(req *http.Request, schemas []int, envelopeVersion int) {
	parts := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		parts = append(parts, fmt.Sprint(schema))
	}
	req.Header.Set("X-Gateway-Snapshot-Schemas", strings.Join(parts, ","))
	req.Header.Set("X-Gateway-Envelope-Version", fmt.Sprint(envelopeVersion))
}

func checksumMatches(raw []byte, want string) bool {
	want = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(want)), "sha256:")
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]) == want
}
