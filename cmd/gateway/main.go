package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Rethinger/2papi/internal/adapter"
	adapteranthropic "github.com/Rethinger/2papi/internal/adapter/anthropic"
	adaptercodex "github.com/Rethinger/2papi/internal/adapter/codex"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/controlplane"
	"github.com/Rethinger/2papi/internal/lite"
	"github.com/Rethinger/2papi/internal/mdns"
	"github.com/Rethinger/2papi/internal/operations"
	"github.com/Rethinger/2papi/internal/resilience"
	"github.com/Rethinger/2papi/internal/server"
	"github.com/Rethinger/2papi/internal/telemetry"

	adapterthirdparty "github.com/Rethinger/2papi/internal/adapter/thirdparty"
)

// Version/Commit/Date are injected at build via -ldflags (see .goreleaser.yaml).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var supportedSnapshotSchemas = []int{1, 2}

const supportedEnvelopeVersion = 2

// defaultHostname is the mDNS/hosts name advertised by `init`/`advert`.
const defaultHostname = "2papi.local"

// AdvertMDNS publishes the gateway over mDNS/Bonjour (used by `2papi advert`
// and `--mdns`); the actual publisher lives in internal/mdns.
func AdvertMDNS(hostname string, port int) (*mdns.Publisher, error) {
	return mdns.NewPublisher(hostname, port, "2papi-gateway")
}

func main() {
	// Subcommand: 2papi version
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Printf("2papi %s (commit %s, built %s)\n", version, commit, date)
		return
	}
	// Subcommand: 2papi tui | menu — interactive control panel (like 9router cli/)
	if len(os.Args) > 1 && (os.Args[1] == "tui" || os.Args[1] == "menu" || os.Args[1] == "ui") {
		if err := RunTUI(); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Subcommand: 2papi init [--hostname X] [--port P]
	if len(os.Args) > 1 && os.Args[1] == "init" {
		hostname := defaultHostname
		port := 8080
		for i, arg := range os.Args {
			if arg == "--hostname" && i+1 < len(os.Args) {
				hostname = os.Args[i+1]
			}
			if len(arg) > 11 && arg[:11] == "--hostname=" {
				hostname = arg[11:]
			}
			if arg == "--port" && i+1 < len(os.Args) {
				if _, err := fmt.Sscanf(os.Args[i+1], "%d", &port); err == nil {
				}
			}
		}
		if err := RunInit(hostname, port); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Subcommand: 2papi advert — keep mDNS advertisement alive.
	if len(os.Args) > 1 && (os.Args[1] == "advert" || os.Args[1] == "advertise") {
		hostname := defaultHostname
		port := 8080
		for i, arg := range os.Args {
			if arg == "--hostname" && i+1 < len(os.Args) {
				hostname = os.Args[i+1]
			}
			if arg == "--port" && i+1 < len(os.Args) {
				_, _ = fmt.Sscanf(os.Args[i+1], "%d", &port)
			}
		}
		pub, err := AdvertMDNS(hostname, port)
		if err != nil {
			log.Fatalf("mDNS unavailable: %v", err)
		}
		log.Printf("advertising %s (mDNS) on port %d — Ctrl+C to stop", hostname, port)
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		pub.Close()
		return
	}
	cfgPath := flag.String("config", "config/example.yaml", "config path")
	mdnsFlag := flag.Bool("mdns", os.Getenv("ADVERTISE_MDNS") == "1", "advertise 2papi.local over mDNS/Bonjour")
	hostnameFlag := flag.String("hostname", defaultHostname, "mDNS hostname (without port)")
	flag.Parse()
	// Try control-plane mode first
	if cp := controlPlaneClientFromEnv(); cp != nil {
		snap, err := config.Load(*cfgPath)
		if err != nil {
			log.Fatal(err)
		}
		st := resilience.New()
		gw := server.NewRuntimeServer(snap, st)
		gw.Version = version
		recorder := telemetry.NewAsyncRecorder(cp, telemetry.AsyncOptions{})
		gw.SetTelemetry(recorder)
		defer func() {
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := recorder.Close(flushCtx); err != nil {
				log.Printf("telemetry flush failed: %v", err)
			}
		}()
		poll := pollIntervalFromEnv()
		trigger := newSnapshotRefreshTrigger()
		installCodexAdapter(gw, cp, trigger)
		installAnthropicAdapter(gw, cp, trigger)
		installThirdpartyAuth(gw, cp, trigger)
		identity := adoptOnce(context.Background(), cp, gw, controlplane.SnapshotIdentity{})
		installCodexAdapter(gw, cp, trigger)
		installAnthropicAdapter(gw, cp, trigger)
		installThirdpartyAuth(gw, cp, trigger)
		go pollControlPlane(cp, gw, poll, identity, trigger)
		rt := gw.Runtime()
		srv := &http.Server{Addr: rt.Snap.Server.Addr, Handler: gw.Routes(), ReadTimeout: rt.Snap.ReadTimeout, WriteTimeout: rt.Snap.WriteTimeout, IdleTimeout: 120 * time.Second}
		internalAddr := os.Getenv("GATEWAY_INTERNAL_ADDR")
		if internalAddr == "" {
			internalAddr = ":8081"
		}
		internalSrv := &http.Server{Addr: internalAddr, Handler: operations.NewDynamicServer(func() *adapter.Registry { return gw.Runtime().Proxy.Registry }, internalTokenFromEnv()).Routes(), ReadTimeout: rt.Snap.ReadTimeout, WriteTimeout: rt.Snap.WriteTimeout, IdleTimeout: 120 * time.Second}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		log.Printf("gateway internal operations listening on %s", internalAddr)
		log.Printf("gateway listening on %s (control-plane mode)", rt.Snap.Server.Addr)
		if *mdnsFlag {
			if pub, err := startMDNS(*hostnameFlag, portOf(rt.Snap.Server.Addr)); err != nil {
				log.Printf("mDNS advertising unavailable: %v", err)
			} else {
				defer pub.Close()
				log.Printf("advertising %s over mDNS (port %d)", *hostnameFlag, portOf(rt.Snap.Server.Addr))
			}
		}
		if err := serveGateway(ctx, srv, internalSrv); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Lite mode: single binary with file-backed store and embedded dashboard
	litePath := os.Getenv("LITE_STORE_PATH")
	if litePath == "" {
		litePath = lite.DefaultPath()
	}
	store, err := lite.New(litePath)
	if err != nil {
		log.Printf("lite store init failed, falling back to file config: %v", err)
		snap, err := config.Load(*cfgPath)
		if err != nil {
			log.Fatal(err)
		}
		st := resilience.New()
		gw := server.NewRuntimeServer(snap, st)
		gw.Version = version
		installCodexAdapter(gw, nil, newSnapshotRefreshTrigger())
		installAnthropicAdapter(gw, nil, newSnapshotRefreshTrigger())
		rt := gw.Runtime()
		srv := &http.Server{Addr: rt.Snap.Server.Addr, Handler: gw.Routes(), ReadTimeout: rt.Snap.ReadTimeout, WriteTimeout: rt.Snap.WriteTimeout, IdleTimeout: 120 * time.Second}
		internalAddr := os.Getenv("GATEWAY_INTERNAL_ADDR")
		if internalAddr == "" {
			internalAddr = ":8081"
		}
		internalSrv := &http.Server{Addr: internalAddr, Handler: operations.NewDynamicServer(func() *adapter.Registry { return gw.Runtime().Proxy.Registry }, internalTokenFromEnv()).Routes(), ReadTimeout: rt.Snap.ReadTimeout, WriteTimeout: rt.Snap.WriteTimeout, IdleTimeout: 120 * time.Second}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if *mdnsFlag {
			if pub, err := startMDNS(*hostnameFlag, portOf(rt.Snap.Server.Addr)); err != nil {
				log.Printf("mDNS advertising unavailable: %v", err)
			} else {
				defer pub.Close()
			}
		}
		log.Printf("gateway listening on %s (lite file mode)", rt.Snap.Server.Addr)
		if err := serveGateway(ctx, srv, internalSrv); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Seed from --config flag if lite.json is fresh from example.yaml but user provided different config
	if *cfgPath != "config/example.yaml" {
		if seedSnap, err := config.Load(*cfgPath); err == nil {
			// If lite store is still default example, override with seed
			if store.Snapshot().Secret == "lite-secret-change-me" || store.Config().Secret != seedSnap.Secret {
				_ = store.Update(func(cfg *config.Config) error { *cfg = seedSnap.Config; return nil })
			}
		}
	}
	snap := store.Snapshot()
	st := resilience.New()
	gw := server.NewRuntimeServer(snap, st)
	gw.Version = version
	store.OnUpdate(func(newSnap *config.Snapshot) { gw.Adopt(newSnap) })
	installCodexAdapter(gw, nil, newSnapshotRefreshTrigger())
	installAnthropicAdapter(gw, nil, newSnapshotRefreshTrigger())
	// Combined handler: lite control-plane API + gateway routes
	liteHandler := lite.NewHandler(store)
	combined := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= 13 && r.URL.Path[:13] == "/api/control" {
			liteHandler.ServeHTTP(w, r)
			return
		}
		gw.Routes().ServeHTTP(w, r)
	})
	rt := gw.Runtime()
	srv := &http.Server{Addr: rt.Snap.Server.Addr, Handler: combined, ReadTimeout: rt.Snap.ReadTimeout, WriteTimeout: rt.Snap.WriteTimeout, IdleTimeout: 120 * time.Second}
	internalAddr := os.Getenv("GATEWAY_INTERNAL_ADDR")
	if internalAddr == "" {
		internalAddr = ":8081"
	}
	internalSrv := &http.Server{Addr: internalAddr, Handler: operations.NewDynamicServer(func() *adapter.Registry { return gw.Runtime().Proxy.Registry }, internalTokenFromEnv()).Routes(), ReadTimeout: rt.Snap.ReadTimeout, WriteTimeout: rt.Snap.WriteTimeout, IdleTimeout: 120 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *mdnsFlag {
		if pub, err := startMDNS(*hostnameFlag, portOf(rt.Snap.Server.Addr)); err != nil {
			log.Printf("mDNS advertising unavailable: %v", err)
		} else {
			defer pub.Close()
			log.Printf("advertising %s over mDNS (port %d)", *hostnameFlag, portOf(rt.Snap.Server.Addr))
		}
	}
	runtimeLogLite()
	log.Printf("gateway internal operations listening on %s", internalAddr)
	log.Printf("gateway listening on %s", rt.Snap.Server.Addr)
	if err := serveGateway(ctx, srv, internalSrv); err != nil {
		log.Fatal(err)
	}
}

// startMDNS advertises hostname on the given port over mDNS.
func startMDNS(hostname string, port int) (*mdns.Publisher, error) {
	return AdvertMDNS(hostname, port)
}

// portOf extracts the port from an addr like ":8080" or "127.0.0.1:8080".
func portOf(addr string) int {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		if n, err := strconv.Atoi(addr[i+1:]); err == nil {
			return n
		}
	}
	return 8080
}

// runtimeLogLite prints a friendly startup line for lite mode.
func runtimeLogLite() {
	log.Printf("2papi lite mode — dashboard embedded, run `2papi tui` for control")
}

func serveGateway(ctx context.Context, servers ...*http.Server) error {
	errs := make(chan error, len(servers))
	for _, server := range servers {
		server := server
		go func() {
			err := server.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			errs <- err
		}()
	}
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errs:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, server := range servers {
		if err := server.Shutdown(shutdownCtx); err != nil && runErr == nil {
			runErr = err
		}
	}
	return runErr
}

func internalTokenFromEnv() string {
	if token := os.Getenv("CONTROL_PLANE_INTERNAL_TOKEN"); token != "" {
		return token
	}
	return os.Getenv("INTERNAL_SERVICE_TOKEN")
}

func controlPlaneClientFromEnv() *controlplane.Client {
	baseURL, token := os.Getenv("CONTROL_PLANE_URL"), os.Getenv("CONTROL_PLANE_INTERNAL_TOKEN")
	if token == "" {
		token = os.Getenv("INTERNAL_SERVICE_TOKEN")
	}
	if !controlplane.Enabled(baseURL, token) {
		return nil
	}
	gwID := os.Getenv("GATEWAY_ID")
	if gwID == "" {
		gwID = "gateway-local"
	}
	return controlplane.New(baseURL, token, gwID)
}

func pollIntervalFromEnv() time.Duration {
	if v := os.Getenv("CONTROL_PLANE_POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 15 * time.Second
}

type snapshotRefreshTrigger struct{ ch chan struct{} }

func newSnapshotRefreshTrigger() *snapshotRefreshTrigger {
	return &snapshotRefreshTrigger{ch: make(chan struct{}, 1)}
}

func (t *snapshotRefreshTrigger) TriggerSnapshotRefresh(reason string) {
	select {
	case t.ch <- struct{}{}:
	default:
	}
}

func installCodexAdapter(gw *server.Server, cp *controlplane.Client, trigger adaptercodex.SnapshotRefreshTrigger) {
	rt := gw.Runtime()
	var sink adaptercodex.CredentialSink
	if cp != nil {
		sink = adaptercodex.ControlPlaneSink{Client: cp}
	}
	_ = adaptercodex.Register(rt.Proxy.Registry, rt.Proxy.Client, sink, trigger, codexOptionsFromEnv())
}

// installAnthropicAdapter re-registers the anthropic adapter with OAuth token
// refresh enabled once the control plane is reachable.
func installAnthropicAdapter(gw *server.Server, cp *controlplane.Client, trigger adapteranthropic.SnapshotRefreshTrigger) {
	rt := gw.Runtime()
	var sink adapteranthropic.CredentialSink
	if cp != nil {
		sink = adapteranthropic.ControlPlaneSink{Client: cp}
	}
	_ = adapteranthropic.RegisterWithAuth(rt.Proxy.Registry, rt.Proxy.Client, sink, trigger)
}

func codexOptionsFromEnv() adaptercodex.Options {
	testMode := os.Getenv("CODEX_TEST_MODE") == "true" || os.Getenv("CODEX_TEST_MODE") == "1"
	if !testMode {
		return adaptercodex.Options{}
	}
	return adaptercodex.Options{
		TestMode:       true,
		AuthBaseURL:    os.Getenv("CODEX_AUTH_ORIGIN"),
		BackendBaseURL: os.Getenv("CODEX_CHATGPT_ORIGIN"),
	}
}

// installThirdpartyAuth re-registers thirdparty OAuth providers (cursor,
// copilot, kimi) with control-plane credential refresh once available.
func installThirdpartyAuth(gw *server.Server, cp *controlplane.Client, trigger adapterthirdparty.Trigger) {
	rt := gw.Runtime()
	var sink adapterthirdparty.Sink
	if cp != nil {
		sink = adapterthirdparty.ControlPlaneSink{Client: cp}
	}
	_ = adapterthirdparty.RegisterOAuth(rt.Proxy.Registry, rt.Proxy.Client, sink, trigger)
}

func pollControlPlane(cp *controlplane.Client, gw *server.Server, interval time.Duration, identity controlplane.SnapshotIdentity, trigger *snapshotRefreshTrigger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-trigger.ch:
		}
		identity = adoptOnce(context.Background(), cp, gw, identity)
		installCodexAdapter(gw, cp, trigger)
		installAnthropicAdapter(gw, cp, trigger)
	}
}

func adoptOnce(ctx context.Context, cp *controlplane.Client, gw *server.Server, current controlplane.SnapshotIdentity) controlplane.SnapshotIdentity {
	if err := cp.Heartbeat(ctx, supportedSnapshotSchemas, supportedEnvelopeVersion); err != nil {
		log.Printf("control-plane heartbeat failed: %v", err)
	}
	snap, identity, err := cp.Fetch(ctx)
	if err != nil {
		log.Printf("control-plane snapshot adoption failed: %v", err)
		if identity.ConfigVersion > 0 || identity.ConfigChecksum != "" || identity.RuntimeChecksum != "" {
			_ = cp.Ack(ctx, controlplane.AckForIdentity(identity, false, err.Error()))
		}
		return current
	}
	if identity.Equal(current) {
		return current
	}
	gw.AdoptVersion(snap, identity.ConfigVersion)
	if err := cp.Ack(ctx, controlplane.AckForIdentity(identity, true, "")); err != nil {
		log.Printf("control-plane snapshot ack failed: %v", err)
		return current
	}
	log.Printf("adopted control-plane snapshot config_version=%d schema_version=%d envelope_version=%d config_checksum=%s runtime_checksum=%s", identity.ConfigVersion, identity.SchemaVersion, identity.EnvelopeVersion, identity.ConfigChecksum, identity.RuntimeChecksum)
	return identity
}
