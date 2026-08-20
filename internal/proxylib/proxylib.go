// Package proxylib parses proxy lists in any practical format and turns
// entries into usable dialers/URLs.
//
// Supported protocols: http, https, socks4, socks4a, socks5, socks5h.
//
// Accepted input formats (mixed freely, separated by newlines, commas or
// semicolons; a JSON array of strings is accepted too):
//
//	http://user:pass@host:8080      https://host:443
//	socks5://host:1080              socks5h://user:pass@host
//	socks4://host:1080              socks4a://host:1080
//	user:pass@host:8080             host:8080
//	host:8080:user:pass             host                 (http, port 80)
//	[::1]:8080                      [::1]:8080:user:pass
//
// Rules:
//   - scheme defaults to http when omitted (port defaults: http 80,
//     https 443, socks* 1080);
//   - path/query/fragment on scheme URLs are stripped (new-api compat);
//   - IPv6 literals must be bracketed;
//   - lines starting with '#' are comments and skipped;
//   - duplicate entries are removed (first occurrence wins);
//   - credentials are never exposed: String() masks the password and the
//     package never logs raw proxy strings.
package proxylib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// Schemes the parser accepts.
const (
	SchemeHTTP    = "http"
	SchemeHTTPS   = "https"
	SchemeSOCKS4  = "socks4"
	SchemeSOCKS4a = "socks4a"
	SchemeSOCKS5  = "socks5"
	SchemeSOCKS5h = "socks5h"
)

var knownSchemes = map[string]bool{
	SchemeHTTP: true, SchemeHTTPS: true,
	SchemeSOCKS4: true, SchemeSOCKS4a: true,
	SchemeSOCKS5: true, SchemeSOCKS5h: true,
}

// Entry is a single normalized proxy.
type Entry struct {
	Scheme string
	Host   string
	Port   int
	User   string
	Pass   string
}

// LineError reports a single invalid line/entry inside a list.
type LineError struct {
	Line   int    // 1-based line number in the raw input
	Text   string // original (trimmed) text of the failing entry
	Reason string // human-readable reason
}

func (e LineError) Error() string { return fmt.Sprintf("line %d %q: %s", e.Line, e.Text, e.Reason) }

// ListError aggregates line errors. It implements error so strict callers
// (config validation) can reject the whole list with one message.
type ListError struct {
	Errors []LineError
}

func (e *ListError) Error() string {
	if len(e.Errors) == 0 {
		return "no proxy entries"
	}
	var b strings.Builder
	for i, le := range e.Errors {
		if i >= 5 {
			fmt.Fprintf(&b, " (and %d more)", len(e.Errors)-5)
			break
		}
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(le.Error())
	}
	return b.String()
}

// IsHTTP reports whether the entry is an http/https proxy (used through
// http.Transport.Proxy).
func (e Entry) IsHTTP() bool { return e.Scheme == SchemeHTTP || e.Scheme == SchemeHTTPS }

// IsSOCKS reports whether the entry is any socks variant.
func (e Entry) IsSOCKS() bool { return !e.IsHTTP() }

// URL returns the proxy URL for http.Transport.Proxy. Credentials are
// included; callers must never log the result.
func (e Entry) URL() *url.URL {
	u := &url.URL{Scheme: e.Scheme, Host: net.JoinHostPort(e.Host, strconv.Itoa(e.Port))}
	if e.User != "" {
		if e.Pass != "" {
			u.User = url.UserPassword(e.User, e.Pass)
		} else {
			u.User = url.User(e.User)
		}
	}
	return u
}

// Key returns the identity used for de-duplication. The password is part of
// the identity: rotating-session providers (bpproxy, oxylabs, …) encode the
// session id in the password, so two entries with the same user/host/port
// but different passwords are different proxies.
func (e Entry) Key() string {
	return fmt.Sprintf("%s://%s:%s@%s:%d", e.Scheme, e.User, e.Pass, e.Host, e.Port)
}

// String returns a masked, log-safe representation: credentials are shown
// as user:****@host:port.
func (e Entry) String() string {
	hostport := net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
	if e.User == "" {
		return e.Scheme + "://" + hostport
	}
	return fmt.Sprintf("%s://%s:****@%s", e.Scheme, e.User, hostport)
}

// Mask masks credentials of a raw proxy string (any accepted format).
// Used by the control plane for display; unknown formats pass through.
func Mask(raw string) string {
	entries, errs := ParseList(raw)
	if len(entries) == 0 {
		return raw
	}
	if len(errs) > 0 {
		// Fall back to a conservative URL-level mask so we never print
		// a password even for otherwise unparseable input.
		return maskURL(raw)
	}
	return entries[0].String()
}

func maskURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.Contains(trimmed, "://") {
		if at := strings.LastIndex(trimmed, "@"); at >= 0 {
			before := trimmed[:at]
			if colon := strings.LastIndex(before, ":"); colon >= 0 {
				return before[:colon] + ":****@" + trimmed[at+1:]
			}
		}
		return trimmed
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return trimmed
	}
	if u.User != nil && u.User.Username() != "" {
		host := u.Host
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		return u.Scheme + "://" + u.User.Username() + ":****@" + host
	}
	return u.Scheme + "://" + u.Host
}

// Parse parses a raw multi-entry list strictly: any invalid entry fails the
// whole list (with the offending lines in the error message).
func Parse(raw string) ([]Entry, error) {
	entries, errs := ParseList(raw)
	if len(errs) > 0 {
		return nil, &ListError{Errors: errs}
	}
	return entries, nil
}

// ParseList parses a raw multi-entry list and reports per-line errors. The
// valid entries are still returned so callers can preview partial results.
func ParseList(raw string) ([]Entry, []LineError) {
	var entries []Entry
	var errs []LineError
	seen := map[string]bool{}

	// Fast path: a JSON array of proxy strings.
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "[") {
		var list []string
		if err := json.Unmarshal([]byte(trimmed), &list); err == nil {
			for i, item := range list {
				addEntry(item, i+1, &entries, &errs, seen)
			}
			return entries, errs
		}
	}

	lines := strings.Split(raw, "\n")
	for lineIndex, line := range lines {
		lineNo := lineIndex + 1
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Inline comment: everything after '#' is ignored.
		if idx := strings.IndexByte(line, '#'); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
			if line == "" {
				continue
			}
		}
		// A line that is a JSON array of strings (e.g. pasted from an API).
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			var list []string
			if err := json.Unmarshal([]byte(line), &list); err == nil {
				for _, item := range list {
					addEntry(item, lineNo, &entries, &errs, seen)
				}
				continue
			}
		}
		for _, token := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\r'
		}) {
			if token == "" {
				continue
			}
			addEntry(token, lineNo, &entries, &errs, seen)
		}
	}
	return entries, errs
}

func addEntry(token string, lineNo int, entries *[]Entry, errs *[]LineError, seen map[string]bool) {
	e, err := ParseEntry(token)
	if err != nil {
		*errs = append(*errs, LineError{Line: lineNo, Text: token, Reason: err.Error()})
		return
	}
	if seen[e.Key()] {
		return
	}
	seen[e.Key()] = true
	*entries = append(*entries, e)
}

// ParseEntry parses a single proxy token in any supported format.
func ParseEntry(token string) (Entry, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Entry{}, errors.New("empty proxy entry")
	}

	// Format with explicit scheme: scheme://user:pass@host[:port][/path]
	if idx := strings.Index(token, "://"); idx >= 0 {
		scheme := strings.ToLower(token[:idx])
		if !knownSchemes[scheme] {
			return Entry{}, fmt.Errorf("unsupported proxy scheme %q", scheme)
		}
		rest := token[idx+3:]
		u, err := url.Parse(scheme + "://" + rest)
		if err != nil {
			return Entry{}, errors.New("malformed proxy URL")
		}
		if u.Hostname() == "" {
			return Entry{}, errors.New("missing proxy host")
		}
		host := u.Hostname()
		port, err := portOf(u.Port(), scheme)
		if err != nil {
			return Entry{}, err
		}
		user, pass := "", ""
		if u.User != nil {
			user = u.User.Username()
			pass, _ = u.User.Password()
		}
		// Reject clearly invalid credentials (control characters would
		// corrupt headers/dialers downstream).
		if err := validateCreds(user, pass); err != nil {
			return Entry{}, err
		}
		return Entry{Scheme: scheme, Host: host, Port: port, User: user, Pass: pass}, nil
	}

	// Formats without a scheme.
	return parseBare(token)
}

// parseBare handles: user:pass@host[:port], host:port[:user:pass],
// [::1]:port[:user:pass], host (defaults to http:80). IPv6 literals must be
// bracketed; a bare token with exactly three ':' and no brackets is read as
// host:port:user:pass.
func parseBare(token string) (Entry, error) {
	user, pass := "", ""
	rest := token

	// user:pass@host — split at the LAST '@' (host names cannot contain '@').
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		userinfo := rest[:at]
		rest = rest[at+1:]
		if userinfo == "" {
			return Entry{}, errors.New("empty proxy credentials")
		}
		sep := strings.IndexByte(userinfo, ':')
		if sep < 0 {
			user = userinfo
		} else {
			user = userinfo[:sep]
			pass = userinfo[sep+1:]
		}
		if user == "" && pass == "" {
			return Entry{}, errors.New("empty proxy credentials")
		}
	}

	var host, portStr string
	// host:port:user:pass (no '@' used, no brackets): exactly 3 colons.
	if user == "" && pass == "" && !strings.HasPrefix(rest, "[") && strings.Count(rest, ":") == 3 {
		parts := strings.Split(rest, ":")
		host, portStr, user, pass = parts[0], parts[1], parts[2], parts[3]
	} else {
		host, portStr, user, pass = splitHostPortBare(rest, user, pass)
	}
	if host == "" {
		return Entry{}, errors.New("missing proxy host")
	}
	port := 80
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil || p < 1 || p > 65535 {
			return Entry{}, fmt.Errorf("invalid proxy port %q", portStr)
		}
		port = p
	}
	if err := validateHost(host); err != nil {
		return Entry{}, err
	}
	if err := validateCreds(user, pass); err != nil {
		return Entry{}, err
	}
	return Entry{Scheme: SchemeHTTP, Host: host, Port: port, User: user, Pass: pass}, nil
}

// splitHostPortBare splits host[:port] or [v6]:port[:user:pass]. For the
// bracketed form a trailing user:pass pair is returned too. Bare IPv6
// without brackets is ambiguous and rejected (documented rule: use brackets).
func splitHostPortBare(token, user, pass string) (host, port, outUser, outPass string) {
	// IPv6 literal in brackets: [::1]:port[:user:pass]
	if strings.HasPrefix(token, "[") {
		end := strings.IndexByte(token, ']')
		if end < 0 {
			return "", "", user, pass
		}
		host = token[1:end]
		rest := token[end+1:]
		if rest == "" {
			return host, "", user, pass
		}
		if !strings.HasPrefix(rest, ":") {
			return "", "", user, pass
		}
		parts := strings.SplitN(rest[1:], ":", 3)
		port = parts[0]
		if len(parts) == 3 && user == "" {
			user, pass = parts[1], parts[2]
		}
		return host, port, user, pass
	}
	if strings.Count(token, ":") > 1 {
		return "", "", user, pass
	}
	parts := strings.Split(token, ":")
	switch len(parts) {
	case 1:
		return parts[0], "", user, pass
	case 2:
		return parts[0], parts[1], user, pass
	default:
		return "", "", user, pass
	}
}

func portOf(port, scheme string) (int, error) {
	if port == "" {
		switch scheme {
		case SchemeHTTP:
			return 80, nil
		case SchemeHTTPS:
			return 443, nil
		default:
			return 1080, nil
		}
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid proxy port %q", port)
	}
	return p, nil
}

func validateHost(host string) error {
	if host == "" {
		return errors.New("missing proxy host")
	}
	if strings.ContainsAny(host, " /\\@") {
		return fmt.Errorf("invalid proxy host %q", host)
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_':
		case r == ':' || r == '[' || r == ']': // IPv6 literal
		default:
			return fmt.Errorf("invalid character in proxy host %q", host)
		}
	}
	return nil
}

func validateCreds(user, pass string) error {
	for _, s := range []string{user, pass} {
		for _, r := range s {
			if r < 0x20 || r == 0x7f {
				return errors.New("proxy credentials contain control characters")
			}
		}
	}
	return nil
}

// DialContext returns a dial function that connects to address through the
// proxy. For socks variants the connection is a raw tunnel; for http/https
// proxies use URL() with http.Transport.Proxy instead.
//
// socks5 resolves the destination locally; socks5h, socks4a and socks4 send
// the hostname to the proxy (remote DNS; socks4 sends an IP — for socks4 the
// hostname is resolved locally, like socks5).
func (e Entry) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch e.Scheme {
	case SchemeSOCKS5, SchemeSOCKS5h:
		return e.dialSOCKS5(ctx, network, address)
	case SchemeSOCKS4, SchemeSOCKS4a:
		return e.dialSOCKS4(ctx, network, address)
	default:
		return nil, fmt.Errorf("scheme %s has no dialer", e.Scheme)
	}
}

func (e Entry) dialSOCKS5(ctx context.Context, network, address string) (net.Conn, error) {
	proxyAddr := net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
	var auth *proxy.Auth
	if e.User != "" {
		auth = &proxy.Auth{User: e.User, Password: e.Pass}
	}
	inner, err := proxy.SOCKS5("tcp", proxyAddr, auth, &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	// socks5 = local DNS: resolve the hostname before handing the address to
	// the proxy. socks5h = remote DNS: pass the hostname through untouched.
	if e.Scheme == SchemeSOCKS5 {
		if resolved, err := resolveLocally(ctx, address); err == nil {
			address = resolved
		} else {
			return nil, err
		}
	}
	if cd, ok := inner.(proxy.ContextDialer); ok {
		return cd.DialContext(ctx, network, address)
	}
	return inner.Dial(network, address)
}

func resolveLocally(ctx context.Context, address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	if net.ParseIP(host) != nil {
		return address, nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", err
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("no addresses for %s", host)
	}
	return net.JoinHostPort(addrs[0].IP.String(), port), nil
}

// dialSOCKS4 implements the SOCKS4/SOCKS4a CONNECT handshake (RFC 1928/1929).
// SOCKS4 has no authentication; a provided password is not sent anywhere.
func (e Entry) dialSOCKS4(ctx context.Context, network, address string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid target port %q", portStr)
	}

	var dstIP net.IP
	var dstHost []byte
	ip := net.ParseIP(host)
	switch {
	case ip == nil:
		// socks4a supports hostnames (remote DNS); socks4 must resolve
		// locally and requires an IPv4 address.
		if e.Scheme == SchemeSOCKS4a {
			dstIP = net.IPv4(0, 0, 0, 1)
			dstHost = []byte(host)
		} else {
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("no IPv4 addresses for %s", host)
			}
			dstIP = ips[0].To4()
			if dstIP == nil {
				return nil, fmt.Errorf("no IPv4 address for %s", host)
			}
		}
	case ip.To4() == nil:
		return nil, errors.New("SOCKS4 does not support IPv6 destinations")
	default:
		dstIP = ip.To4()
	}

	proxyAddr := net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
	conn, err := (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, proxyAddr)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (net.Conn, error) { _ = conn.Close(); return nil, err }

	// Request: VN=4, CD=1(CONNECT), DSTPORT(2), DSTIP(4), USERID\0, [4a: HOST\0]
	req := make([]byte, 0, 9+len(e.User)+1+len(dstHost)+1)
	req = append(req, 4, 1, byte(port>>8), byte(port))
	req = append(req, dstIP.To4()...)
	req = append(req, []byte(e.User)...)
	req = append(req, 0)
	if len(dstHost) > 0 {
		req = append(req, dstHost...)
		req = append(req, 0)
	}
	if _, err := conn.Write(req); err != nil {
		return fail(err)
	}

	reply := make([]byte, 8)
	if _, err := readFull(ctx, conn, reply); err != nil {
		return fail(err)
	}
	if reply[0] != 0 {
		return fail(fmt.Errorf("SOCKS4 proxy returned invalid version %d", reply[0]))
	}
	switch reply[1] {
	case 90:
		return conn, nil
	case 91:
		return fail(errors.New("SOCKS4 request rejected or failed"))
	case 92:
		return fail(errors.New("SOCKS4 request rejected: cannot reach identd"))
	case 93:
		return fail(errors.New("SOCKS4 request rejected: user-id mismatch"))
	default:
		return fail(fmt.Errorf("SOCKS4 request failed with status %d", reply[1]))
	}
}

func readFull(ctx context.Context, conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
	}
	return total, nil
}
