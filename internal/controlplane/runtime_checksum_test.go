package controlplane

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeSnapshotV2FixtureChecksumMatchesRawBytes(t *testing.T) {
	fixture := filepath.Join("..", "..", "test", "fixtures", "runtime-snapshot-v2.json")
	hashFixture := filepath.Join("..", "..", "test", "fixtures", "runtime-snapshot-v2.sha256")
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(hashFixture)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	sum := sha256.Sum256(raw)
	if got, want := hex.EncodeToString(sum[:]), string(bytes.TrimSpace(expected)); got != want {
		t.Fatalf("checksum=%s want %s", got, want)
	}
}
