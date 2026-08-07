package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/1jehuang/2papi/internal/adapter"
	adaptercodex "github.com/1jehuang/2papi/internal/adapter/codex"
	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/controlplane"
	"github.com/1jehuang/2papi/internal/operations"
	"github.com/1jehuang/2papi/internal/resilience"
	"github.com/1jehuang/2papi/internal/server"
)

var supportedSnapshotSchemas = []int{1, 2}

const supportedEnvelopeVersion = 2

func main() {
	cfg := flag.String("config", "config/example.yaml", "config path")
	flag.Parse()
	snap, err := config.Load(*cfg)
	if err != nil {
		log.Fatal(err)
	}
	st := resilience.New()
	gw := server.NewRuntimeServer(snap, st)
	if cp := controlPlaneClientFromEnv(); cp != nil {
		poll := pollIntervalFromEnv()
		trigger := newSnapshotRefreshTrigger()
		installCodexAdapter(gw, cp, trigger)
		identity := adoptOnce(context.Background(), cp, gw, controlplane.SnapshotIdentity{})
		installCodexAdapter(gw, cp, trigger)
		go pollControlPlane(cp, gw, poll, identity, trigger)
	} else {
		installCodexAdapter(gw, nil, newSnapshotRefreshTrigger())
	}
	rt := gw.Runtime()
	srv := &http.Server{Addr: rt.Snap.Server.Addr, Handler: gw.Routes(), ReadTimeout: rt.Snap.ReadTimeout, WriteTimeout: rt.Snap.WriteTimeout}
	internalAddr := os.Getenv("GATEWAY_INTERNAL_ADDR")
	if internalAddr == "" {
		internalAddr = ":8081"
	}
	internalSrv := &http.Server{Addr: internalAddr, Handler: operations.NewDynamicServer(func() *adapter.Registry { return gw.Runtime().Proxy.Registry }, internalTokenFromEnv()).Routes(), ReadTimeout: rt.Snap.ReadTimeout, WriteTimeout: rt.Snap.WriteTimeout}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("gateway internal operations listening on %s", internalAddr)
	log.Printf("gateway listening on %s", rt.Snap.Server.Addr)
	if err := serveGateway(ctx, srv, internalSrv); err != nil {
		log.Fatal(err)
	}
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
	_ = adaptercodex.Register(rt.Proxy.Registry, rt.Proxy.Client, sink, trigger, adaptercodex.Options{})
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
	gw.Adopt(snap)
	if err := cp.Ack(ctx, controlplane.AckForIdentity(identity, true, "")); err != nil {
		log.Printf("control-plane snapshot ack failed: %v", err)
		return current
	}
	log.Printf("adopted control-plane snapshot config_version=%d schema_version=%d envelope_version=%d config_checksum=%s runtime_checksum=%s", identity.ConfigVersion, identity.SchemaVersion, identity.EnvelopeVersion, identity.ConfigChecksum, identity.RuntimeChecksum)
	return identity
}
