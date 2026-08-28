package protocol

import (
	"io"
	"strings"
	"testing"
)

func readAll(t *testing.T, src io.ReadCloser) string {
	t.Helper()
	b, err := io.ReadAll(src)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSSEModelRewriteReplacesUpstreamWithAlias(t *testing.T) {
	events := "data: {\"model\":\"accounts/fw/deep-v4\",\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n" +
		"data: {\"model\": \"accounts/fw/deep-v4\", \"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n" +
		"data: [DONE]\n\n"
	out := readAll(t, NewSSEModelRewriteReader(io.NopCloser(strings.NewReader(events)), "accounts/fw/deep-v4", "deep-flash"))
	if strings.Contains(out, "accounts/fw/deep-v4") {
		t.Fatalf("upstream id leaked: %s", out)
	}
	if strings.Count(out, "deep-flash") != 2 {
		t.Fatalf("both spaced and compact forms rewritten: %s", out)
	}
	if !strings.Contains(out, "\"content\":\"he\"") || !strings.Contains(out, "\"content\":\"llo\"") {
		t.Fatalf("payload must survive untouched: %s", out)
	}
	if !strings.Contains(out, "data: [DONE]\n") {
		t.Fatalf("terminator preserved: %s", out)
	}
}

func TestSSEModelRewriteHandlesLinesSplitAcrossReads(t *testing.T) {
	full := "data: {\"model\":\"up-x\",\"choices\":[{\"delta\":{\"content\":\"abc def ghi jkl\"}}]}\n\n"
	// Split mid-line at an arbitrary byte to simulate network chunking.
	splitAt := 17
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte(full[:splitAt]))
		pw.Write([]byte(full[splitAt:]))
		pw.Close()
	}()
	out := readAll(t, NewSSEModelRewriteReader(pr, "up-x", "alias-y"))
	if strings.Contains(out, "up-x") || !strings.Contains(out, `"model":"alias-y"`) {
		t.Fatalf("split line must still be rewritten: %q", out)
	}
	if !strings.Contains(out, "abc def ghi jkl") {
		t.Fatalf("payload intact: %q", out)
	}
}

func TestSSEModelRewritePassesThroughWhenNamesMatch(t *testing.T) {
	events := "data: {\"model\":\"same\",\"x\":1}\n\n"
	src := io.NopCloser(strings.NewReader(events))
	// No wrapping expected when upstream == alias; the constructor returns src.
	if got := NewSSEModelRewriteReader(src, "same", "same"); got != src {
		t.Fatal("identical names must bypass the rewriter entirely")
	}
}
