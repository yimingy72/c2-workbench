package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"c2-platform/internal/api"
	"c2-platform/internal/config"
	"c2-platform/internal/listener"
	"c2-platform/internal/session"
	"c2-platform/webui"
)

func main() {
	cfgPath := flag.String("config", "conf/setting.conf", "path to setting.conf")
	flag.Parse()
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	signal.Ignore(syscall.SIGHUP)

	hub := session.NewHub(nil)
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for range t.C {
			hub.Tick()
		}
	}()
	lm := listener.NewManager(hub)
	lm.SetPersist("data/listeners.json")
	lm.Restore()
	h := api.New(cfg, hub, lm, webui.Handler())
	srv := &http.Server{
		Addr:              cfg.WebAddr(),
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("console http://%s  title=%s", cfg.WebAddr(), cfg.WebTitle)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("listen: %v", err)
			os.Exit(1)
		}
	}()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	fmt.Fprintln(os.Stderr, "shutting down")
	_ = srv.Close()
}
