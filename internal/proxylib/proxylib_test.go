package proxylib

import (
	"strings"
	"testing"
)

func TestParseEntryFormats(t *testing.T) {
	tests := []struct {
		in     string
		scheme string
		host   string
		port   int
		user   string
		pass   string
	}{
		{"http://user:pass@host:8080", "http", "host", 8080, "user", "pass"},
		{"https://host:443", "https", "host", 443, "", ""},
		{"https://host", "https", "host", 443, "", ""},
		{"socks5://host:1080", "socks5", "host", 1080, "", ""},
		{"socks5://host", "socks5", "host", 1080, "", ""},
		{"socks5h://user:pass@host", "socks5h", "host", 1080, "user", "pass"},
		{"socks4://host:1080", "socks4", "host", 1080, "", ""},
		{"socks4a://host:1080", "socks4a", "host", 1080, "", ""},
		{"socks4a://u@h:9", "socks4a", "h", 9, "u", ""},
		{"user:pass@host:8080", "http", "host", 8080, "user", "pass"},
		{"host:8080", "http", "host", 8080, "", ""},
		{"host", "http", "host", 80, "", ""},
		{"host:8080:user:pass", "http", "host", 8080, "user", "pass"},
		{"10.0.0.1:3128", "http", "10.0.0.1", 3128, "", ""},
		{"10.0.0.1:3128:u:p", "http", "10.0.0.1", 3128, "u", "p"},
		{"[::1]:8080", "http", "::1", 8080, "", ""},
		{"[2001:db8::1]:8080:user:pass", "http", "2001:db8::1", 8080, "user", "pass"},
		{"[::1]", "http", "::1", 80, "", ""},
		{"user@host:8080", "http", "host", 8080, "user", ""},
		{"http://host:8080/path?query#frag", "http", "host", 8080, "", ""},
		{"http://user:pass@host:8080/path", "http", "host", 8080, "user", "pass"},
		{"SOCKS5://HOST:1080", "socks5", "HOST", 1080, "", ""},
	}
	for _, tt := range tests {
		e, err := ParseEntry(tt.in)
		if err != nil {
			t.Errorf("ParseEntry(%q) unexpected error: %v", tt.in, err)
			continue
		}
		if e.Scheme != tt.scheme || e.Host != tt.host || e.Port != tt.port || e.User != tt.user || e.Pass != tt.pass {
			t.Errorf("ParseEntry(%q) = {%s %s %d %q %q}, want {%s %s %d %q %q}",
				tt.in, e.Scheme, e.Host, e.Port, e.User, e.Pass,
				tt.scheme, tt.host, tt.port, tt.user, tt.pass)
		}
	}
}

func TestParseEntryInvalid(t *testing.T) {
	invalid := []string{
		"",
		"   ",
		"not a proxy!!",
		"host:99999",
		"host:0",
		"host:port:user",     // 3 parts unsupported
		"http://",            // no host
		"http://:8080",       // empty host
		"ftp://host:21",      // unknown scheme
		"socks6://host:1080", // unknown scheme
		"user:pass@",         // empty host
		"@host:8080",         // empty user info
		"host:8080:user:",    // empty pass — allowed? validateCreds allows empty pass; colon form with empty pass: user="user", pass="" — hmm
		"2001:db8::1",        // bare IPv6 without brackets
		"[::1",               // unclosed bracket
		"ho st:8080",         // space in host — wait, splitLines already splits on spaces; ParseEntry directly on "ho st:8080"...
		"host/evil:8080",     // slash in host
	}
	// "host:8080:user:" — parts split → ["host","8080","user",""] → user="user", pass="" — passes validation.
	// Not in the invalid list by design (empty password with a user is a valid proxy auth).
	for _, in := range invalid {
		if in == "host:8080:user:" {
			continue
		}
		if _, err := ParseEntry(in); err == nil {
			t.Errorf("ParseEntry(%q) expected error, got none", in)
		}
	}
}

func TestParseEntryAllowsEmptyPassWithUser(t *testing.T) {
	e, err := ParseEntry("host:8080:user:")
	if err != nil {
		t.Fatalf("host:8080:user: should parse: %v", err)
	}
	if e.User != "user" || e.Pass != "" {
		t.Fatalf("got user=%q pass=%q", e.User, e.Pass)
	}
}

func TestParseListMixedFormats(t *testing.T) {
	raw := `# global pool
http://user:pass@host-a:8080
host-b:3128
socks5://host-c:1080, socks4a://host-d:1080; user:p@host-e:8080
host-f:8080:u1:p1 # inline comment
[::1]:9090
["http://json-a:1", "socks5://json-b:2"]`
	entries, errs := ParseList(raw)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := []struct {
		scheme, host string
		port         int
	}{
		{"http", "host-a", 8080},
		{"http", "host-b", 3128},
		{"socks5", "host-c", 1080},
		{"socks4a", "host-d", 1080},
		{"http", "host-e", 8080},
		{"http", "host-f", 8080},
		{"http", "::1", 9090},
		{"http", "json-a", 1},
		{"socks5", "json-b", 2},
	}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(entries), len(want), entries)
	}
	for i, w := range want {
		if entries[i].Scheme != w.scheme || entries[i].Host != w.host || entries[i].Port != w.port {
			t.Errorf("entry[%d] = %s://%s:%d, want %s://%s:%d", i, entries[i].Scheme, entries[i].Host, entries[i].Port, w.scheme, w.host, w.port)
		}
	}
}

func TestParseListDedupe(t *testing.T) {
	raw := "host-a:1\nhost-a:1\nhost-a:1:u:p\nhost-b:2"
	entries, errs := ParseList(raw)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3 (dedupe + distinct user): %+v", len(entries), entries)
	}
}

func TestParseListKeepsSessionPasswords(t *testing.T) {
	raw := "socks5://u:p_session-AAA@host:2002\nsocks5://u:p_session-BBB@host:2002\nsocks5://u:p_session-AAA@host:2002"
	entries, errs := ParseList(raw)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 distinct session passwords: %+v", len(entries), entries)
	}
	if entries[0].Pass == entries[1].Pass {
		t.Fatal("session passwords collapsed")
	}
}

func TestParseListLineErrors(t *testing.T) {
	raw := "host-a:1\nhost-b:99999\nhost-c:2"
	entries, errs := ParseList(raw)
	if len(entries) != 2 {
		t.Fatalf("got %d valid entries, want 2", len(entries))
	}
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want 1", len(errs))
	}
	if errs[0].Line != 2 {
		t.Errorf("error line = %d, want 2", errs[0].Line)
	}
	if !strings.Contains(errs[0].Reason, "port") {
		t.Errorf("error text = %q, want port mention", errs[0].Reason)
	}
}

func TestParseStrict(t *testing.T) {
	if _, err := Parse("host-a:1\nhost-b:99999"); err == nil {
		t.Fatal("Parse with invalid entry should fail")
	}
	entries, err := Parse("host-a:1\nsocks5://host-b:2")
	if err != nil {
		t.Fatalf("Parse valid list failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
}

func TestEntryStringMasksPassword(t *testing.T) {
	e, _ := ParseEntry("http://user:secret@host:8080")
	if s := e.String(); s != "http://user:****@host:8080" {
		t.Errorf("String() = %q", s)
	}
	e2, _ := ParseEntry("host:8080")
	if s := e2.String(); s != "http://host:8080" {
		t.Errorf("String() = %q", s)
	}
	e3, _ := ParseEntry("socks5://u:p@[::1]:1080")
	if s := e3.String(); s != "socks5://u:****@[::1]:1080" {
		t.Errorf("String() = %q", s)
	}
}

func TestMaskNeverLeaksPassword(t *testing.T) {
	cases := []string{
		"http://user:secret@host:8080",
		"user:secret@host:8080",
		"host:8080:user:secret",
		"socks5://user:secret@host",
		"user:secret@host", // bare, default port
	}
	for _, in := range cases {
		masked := Mask(in)
		if strings.Contains(masked, "secret") {
			t.Errorf("Mask(%q) leaked password: %q", in, masked)
		}
	}
	// Unparseable input must still not leak a password after '@'.
	if masked := Mask("weird : stuff user:secret@host"); strings.Contains(masked, "secret") {
		t.Errorf("Mask fallback leaked password: %q", masked)
	}
}

func TestEntryURL(t *testing.T) {
	e, _ := ParseEntry("http://user:pass@host:8080")
	u := e.URL()
	if u.Scheme != "http" || u.Host != "host:8080" {
		t.Errorf("URL = %s", u)
	}
	gotPass, _ := u.User.Password()
	if u.User.Username() != "user" || gotPass != "pass" {
		t.Errorf("URL credentials = %s", u.User)
	}
	e2, _ := ParseEntry("host:8080")
	if u2 := e2.URL(); u2.Host != "host:8080" || u2.User != nil {
		t.Errorf("URL = %s", u2)
	}
}
