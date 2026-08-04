package main

import (
	"flag"
	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/proxy"
	"github.com/1jehuang/2papi/internal/resilience"
	"github.com/1jehuang/2papi/internal/router"
	"github.com/1jehuang/2papi/internal/server"
	"log"
	"net/http"
)

func main() {
	cfg := flag.String("config", "config/example.yaml", "config path")
	flag.Parse()
	snap, err := config.Load(*cfg)
	if err != nil {
		log.Fatal(err)
	}
	st := resilience.New()
	rt := router.New(snap, st)
	px := proxy.New(snap, st, rt)
	srv := &http.Server{Addr: snap.Server.Addr, Handler: server.New(snap, px).Routes(), ReadTimeout: snap.ReadTimeout, WriteTimeout: snap.WriteTimeout}
	log.Printf("gateway listening on %s", snap.Server.Addr)
	log.Fatal(srv.ListenAndServe())
}
