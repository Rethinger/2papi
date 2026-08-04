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

type SnapshotEnvelope struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Snapshot json.RawMessage `json:"snapshot"`
	Config   json.RawMessage `json:"config"`
}

type Ack struct {
	GatewayID string `json:"gateway_id"`
	Version   int    `json:"version"`
	Checksum  string `json:"checksum"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

func Enabled(baseURL, token string) bool {
	return strings.TrimSpace(baseURL) != "" && strings.TrimSpace(token) != ""
}

func New(baseURL, token, gatewayID string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, GatewayID: gatewayID, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) Fetch(ctx context.Context) (*config.Snapshot, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/internal/v1/snapshot", nil)
	if err != nil {
		return nil, 0, "", err
	}
	c.authorize(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, 0, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, 0, "", fmt.Errorf("snapshot fetch status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var env SnapshotEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, 0, "", err
	}
	raw := env.Snapshot
	if len(raw) == 0 {
		raw = env.Config
	}
	if len(raw) == 0 {
		return nil, 0, "", fmt.Errorf("snapshot payload missing")
	}
	if env.Version <= 0 {
		return nil, 0, "", fmt.Errorf("snapshot version required")
	}
	if env.Checksum == "" {
		return nil, 0, "", fmt.Errorf("snapshot checksum required")
	}
	if !checksumMatches(raw, env.Checksum) {
		return nil, env.Version, env.Checksum, fmt.Errorf("snapshot checksum mismatch")
	}
	var cfg config.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, env.Version, env.Checksum, err
	}
	snap, err := config.Build(cfg)
	if err != nil {
		return nil, env.Version, env.Checksum, err
	}
	return snap, env.Version, env.Checksum, nil
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

func (c *Client) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Internal-Service-Token", c.Token)
	if c.GatewayID != "" {
		req.Header.Set("X-Gateway-ID", c.GatewayID)
	}
}

func checksumMatches(raw []byte, want string) bool {
	want = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(want)), "sha256:")
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) == want {
		return true
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		canon, _ := json.Marshal(v)
		sum = sha256.Sum256(canon)
		return hex.EncodeToString(sum[:]) == want
	}
	return false
}
