package main

import (
	"context"
	"flag"
	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/controlplane"
	"github.com/1jehuang/2papi/internal/resilience"
	"github.com/1jehuang/2papi/internal/server"
	"log"
	"net/http"
	"os"
	"time"
)

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
		version, checksum := adoptOnce(context.Background(), cp, gw, 0, "")
		go pollControlPlane(cp, gw, poll, version, checksum)
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

func pollControlPlane(cp *controlplane.Client, gw *server.Server, interval time.Duration, version int, checksum string) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		version, checksum = adoptOnce(context.Background(), cp, gw, version, checksum)
	}
}

func adoptOnce(ctx context.Context, cp *controlplane.Client, gw *server.Server, currentVersion int, currentChecksum string) (int, string) {
	snap, version, checksum, err := cp.Fetch(ctx)
	if err != nil {
		log.Printf("control-plane snapshot adoption failed: %v", err)
		if version > 0 && checksum != "" {
			_ = cp.Ack(ctx, controlplane.Ack{Version: version, Checksum: checksum, Success: false, Error: err.Error()})
		}
		return currentVersion, currentChecksum
	}
	if version == currentVersion && checksum == currentChecksum {
		return currentVersion, currentChecksum
	}
	gw.Adopt(snap)
	if err := cp.Ack(ctx, controlplane.Ack{Version: version, Checksum: checksum, Success: true}); err != nil {
		log.Printf("control-plane snapshot ack failed: %v", err)
	}
	log.Printf("adopted control-plane snapshot version=%d checksum=%s", version, checksum)
	return version, checksum
}
