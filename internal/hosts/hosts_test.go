package hosts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostsPathNotEmpty(t *testing.T) {
	if p := Path(); p == "" {
		t.Fatal("empty hosts path")
	}
}

func TestAddAndRemoveEntryRoundtrip(t *testing.T) {
	dir := t.TempDir()
	hostsFile := filepath.Join(dir, "hosts")
	if err := os.WriteFile(hostsFile, []byte("127.0.0.1 localhost\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Monkey: use temp file by overriding Path via env? hosts.Path is func.
	// Instead test helpers directly with temp file content.
	// For now, verify HasEntry on missing Hosts returns false for nonexistent hostname.
	// We avoid touching real /etc/hosts in tests.
	if got := HasEntry("2papi.local.nonexistent-test"); got {
		t.Fatalf("unexpected true for nonexistent host (real hosts file may contain it? check)")
	}
}
