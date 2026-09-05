package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Rethinger/squoze"
)

// TestDedupDiagnostic prints the per-turn, per-message shape of a growing agent
// session so the cross-turn dedup and distillation behaviour is observable
// rather than inferred.
func TestDedupDiagnostic(t *testing.T) {
	turns := MultiTurnSession("claude-opus-4-5")
	eng := squoze.NewEngine(squoze.DefaultMemoCapacity)

	for ti, tn := range turns {
		cp := append([]byte(nil), tn...)
		out, res := eng.Apply(cp)
		in := messageContents(tn)
		got := messageContents(out)

		t.Logf("── turn %d: body %d -> %d bytes, blocks_squeezed=%d memo_hits=%d",
			ti+1, len(tn), len(out), res.BlocksSqueezed, res.MemoHits)
		for i := range got {
			shape := "verbatim"
			switch {
			case strings.Contains(got[i].Content, "earlier view"):
				shape = "DEDUP-MARKER"
			case strings.Contains(got[i].Content, "middle lines elided"):
				shape = "ELIDED"
			case len(got[i].Content) != len(in[i].Content):
				shape = "CHANGED-other"
			}
			t.Logf("   msg[%d] role=%-6s in=%7d out=%7d  %s",
				i, got[i].Role, len(in[i].Content), len(got[i].Content), shape)
		}
	}
}

// TestDedupOnPreCompressedHistory models the realistic agent flow the cache
// contract cares about: the client resends what it was previously SENT
// (already elided), not the pristine original. This is the shape a proxy sees
// when the agent framework keeps the gateway's response history.
func TestDedupOnPreCompressedHistory(t *testing.T) {
	eng := squoze.NewEngine(squoze.DefaultMemoCapacity)
	file := goSourceWithTestWords()

	// Turn 1: single file read.
	t1 := buildBody("claude-opus-4-5", []msg{
		{"user", "fix the total"},
		{"tool", file},
	})
	out1, _ := eng.Apply(append([]byte(nil), t1...))
	sent1 := messageContents(out1)
	t.Logf("turn 1: tool msg %d -> %d bytes", len(file), len(sent1[1].Content))

	// Turn 2: client echoes back what it was sent, then adds a fresh read of
	// the same file.
	t2 := buildBody("claude-opus-4-5", []msg{
		{"user", "fix the total"},
		{"tool", sent1[1].Content}, // already elided
		{"tool", file},             // fresh full read
	})
	out2, _ := eng.Apply(append([]byte(nil), t2...))
	sent2 := messageContents(out2)

	t.Logf("turn 2: msg1 %d -> %d bytes, msg2 %d -> %d bytes",
		len(sent1[1].Content), len(sent2[1].Content), len(file), len(sent2[2].Content))

	if sent2[1].Content != sent1[1].Content {
		t.Errorf("PREFIX BROKEN: the already-sent history block was rewritten (%d -> %d bytes)",
			len(sent1[1].Content), len(sent2[1].Content))
	} else {
		t.Logf("history block byte-stable across turns — cache prefix preserved")
	}
}

type msg struct{ role, content string }

func buildBody(model string, msgs []msg) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"` + model + `","messages":[`)
	for i, m := range msgs {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"role":"` + m.role + `","content":`)
		b.WriteString(jsonQuote(m.content))
		b.WriteString("}")
	}
	b.WriteString("]}")
	return []byte(b.String())
}

func jsonQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(fmt.Sprintf(`\u%04x`, r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
