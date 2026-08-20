package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/proxylib"
)

// Proxy routing design:
//
//   - every Account may carry its own proxy list (any format, all protocols);
//   - a global pool (snapshot.Proxies) is the fallback for accounts without
//     their own proxy;
//   - requests without any proxy go direct (HTTP_PROXY env respected);
//   - adapters build upstream requests with the ctx passed to Execute, so the
//     pool transport reads the account's proxy group straight from the
//     request context — no adapter changes needed;
//   - rotation is round-robin per request, with dial-error failover to the
//     next proxy in the group (safe: nothing was written yet);
//   - one http.Transport per proxy keeps connection pools isolated.
//
// Context keys are unexported; InjectGroup/GroupFromContext/BypassPool are
// exported so the internal provider-operations server can route too.

type proxyGroupKey struct{}
type proxyBypassKey struct{}
type proxyUseKey struct{}

// proxyUse records (masked) which proxy actually served the request so the
// gateway can surface X-Gateway-Proxy.
type proxyUse struct{ used string }

func (u *proxyUse) Used() string {
	if u == nil {
		return ""
	}
	return u.used
}

// proxyGroup is an immutable set of proxy transports. One transport per
// entry: pooled connections never mix between proxies.
type proxyGroup struct {
	entries    []proxylib.Entry
	transports []*http.Transport
}

// BuildGroup compiles entries into an isolated proxy group. The returned
// group is immutable and safe for concurrent use.
func BuildGroup(entries []proxylib.Entry) *proxyGroup {
	g := &proxyGroup{}
	for _, e := range entries {
		g.entries = append(g.entries, e)
		g.transports = append(g.transports, transportForEntry(e))
	}
	return g
}

// CloseIdleConnections closes idle pooled connections of all transports in
// the group (used for per-request groups built by the operations server).
func (g *proxyGroup) CloseIdleConnections() {
	if g == nil {
		return
	}
	for _, t := range g.transports {
		t.CloseIdleConnections()
	}
}

func (g *proxyGroup) len() int {
	if g == nil {
		return 0
	}
	return len(g.transports)
}

// InjectGroup attaches a proxy group to the context. A nil group leaves the
// context untouched (global pool / direct fallback apply downstream).
func InjectGroup(ctx context.Context, g *proxyGroup) context.Context {
	if g == nil || g.len() == 0 {
		return ctx
	}
	return context.WithValue(ctx, proxyGroupKey{}, g)
}

// GroupFromContext returns the group attached to the context, if any.
func GroupFromContext(ctx context.Context) *proxyGroup {
	g, _ := ctx.Value(proxyGroupKey{}).(*proxyGroup)
	return g
}

// BypassPool disables proxy routing for the request (webhooks etc.).
func BypassPool(ctx context.Context) context.Context {
	return context.WithValue(ctx, proxyBypassKey{}, true)
}

// WithProxyUse attaches a holder the transport fills with the masked proxy
// actually used, so the caller can surface it in a response header.
func WithProxyUse(ctx context.Context) (context.Context, *proxyUse) {
	u := &proxyUse{}
	return context.WithValue(ctx, proxyUseKey{}, u), u
}

func transportForEntry(e proxylib.Entry) *http.Transport {
	t := &http.Transport{
		MaxIdleConns:          512,
		MaxIdleConnsPerHost:   128,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if e.IsHTTP() {
		u := e.URL()
		t.Proxy = func(*http.Request) (*url.URL, error) { return u, nil }
	} else {
		t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return e.DialContext(ctx, network, address)
		}
	}
	return t
}

// poolTransport routes requests through the request's proxy group (per
// account) or the global pool, with round-robin rotation and dial-error
// failover; requests without a group go direct.
//
// The transport is meant to live for the whole server lifetime: adapters
// hold the shared client, so on snapshot adoption only the global pool is
// swapped (SetGlobal) instead of replacing the client.
type poolTransport struct {
	direct *http.Transport
	global atomic.Pointer[proxyGroup]
	rr     atomic.Uint64
}

// PoolTransport is the exported name of the proxy-aware RoundTripper used as
// the shared client's Transport (kept for tests/introspection).
type PoolTransport = poolTransport

// Direct returns the underlying direct transport (HTTP_PROXY env respected).
func (p *poolTransport) Direct() *http.Transport { return p.direct }

// SetGlobal atomically swaps the global proxy pool (snapshot adoption).
func (p *poolTransport) SetGlobal(g *proxyGroup) { p.global.Store(g) }

func buildPoolTransport(snap *config.Snapshot) *poolTransport {
	pt := &poolTransport{direct: newSharedTransport()}
	if snap != nil && len(snap.GlobalProxies) > 0 {
		pt.global.Store(BuildGroup(snap.GlobalProxies))
	}
	return pt
}

func (p *poolTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, bypass := req.Context().Value(proxyBypassKey{}).(bool); bypass {
		return p.direct.RoundTrip(req)
	}
	group := GroupFromContext(req.Context())
	if group == nil || group.len() == 0 {
		group = p.global.Load()
	}
	if group == nil || group.len() == 0 {
		return p.direct.RoundTrip(req)
	}
	n := group.len()
	start := int((p.rr.Add(1) - 1) % uint64(n))
	use, _ := req.Context().Value(proxyUseKey{}).(*proxyUse)
	var lastErr error
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		resp, err := group.transports[idx].RoundTrip(req)
		if err == nil {
			if use != nil {
				use.used = group.entries[idx].String()
			}
			return resp, nil
		}
		lastErr = err
		// Fail over only on connection-level errors (before any request
		// bytes were written); anything else is not safe to retry.
		if !isDialError(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// isDialError reports whether the error happened during the dial/proxy
// connect phase, before any request bytes were written.
func isDialError(err error) bool {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

func newSharedTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          512,
		MaxIdleConnsPerHost:   128,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
}
