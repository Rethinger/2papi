package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/controlplane"
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
		identity := adoptOnce(context.Background(), cp, gw, controlplane.SnapshotIdentity{})
		go pollControlPlane(cp, gw, poll, identity)
	}
	rt := gw.Runtime()
	srv := &http.Server{Addr: rt.Snap.Server.Addr, Handler: gw.Routes(), ReadTimeout: rt.Snap.ReadTimeout, WriteTimeout: rt.Snap.WriteTimeout}
	log.Printf("gateway listening on %s", rt.Snap.Server.Addr)
	log.Fatal(srv.ListenAndServe())
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

func pollControlPlane(cp *controlplane.Client, gw *server.Server, interval time.Duration, identity controlplane.SnapshotIdentity) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		identity = adoptOnce(context.Background(), cp, gw, identity)
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
	}
	log.Printf("adopted control-plane snapshot config_version=%d schema_version=%d envelope_version=%d config_checksum=%s runtime_checksum=%s", identity.ConfigVersion, identity.SchemaVersion, identity.EnvelopeVersion, identity.ConfigChecksum, identity.RuntimeChecksum)
	return identity
}
