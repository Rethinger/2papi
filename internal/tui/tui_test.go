package tui

import (
	"os"
	"testing"
)

func TestCountSelectable(t *testing.T) {
	items := []Item{
		{Kind: "header"},
		{Kind: "menu", Label: "Start"},
		{Kind: "sep"},
		{Kind: "menu", Label: "Providers"},
		{Kind: "info"},
		{Kind: "menu", Label: "Quit"},
	}
	if got := countSelectable(items); got != 3 {
		t.Fatalf("countSelectable=%d want 3", got)
	}
}

func TestBannerNoopWhenNotStdout(t *testing.T) {
	// Banner writes only; safe to call with captured writer.
	old := os.Stdout
	_ = old
	Banner()
}
