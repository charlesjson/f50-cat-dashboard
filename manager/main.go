package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const version = "0.1.0"

func main() {
	var options Options
	var listen, devOrigin string
	var initialize bool
	var checkOnly, reloadOnly bool
	flag.StringVar(&options.Root, "state-dir", "/data/clash/Manager", "Private state and backups")
	flag.StringVar(&options.Config, "config", "/data/clash/Proxy/config.yaml", "Mihomo config")
	flag.StringVar(&options.Core, "core", "/data/clash/Proxy/Clash.Core", "Mihomo binary")
	flag.StringVar(&options.Controller, "controller", "http://127.0.0.1:7788", "Local Mihomo API")
	flag.StringVar(&options.Service, "service", "/data/clash/Scripts/Clash.Service", "F50 service script")
	flag.StringVar(&options.Curl, "curl", "/data/data/com.minikano.f50_sms/files/curl", "Curl binary; empty uses Go HTTP")
	flag.StringVar(&options.FastHash, "fast-hash", "/data/clash/Scripts/fast_sha256", "F50 fast-start config hash; empty disables")
	flag.StringVar(&listen, "listen", "0.0.0.0:7790", "IPv4 LAN management address")
	flag.StringVar(&devOrigin, "dev-origin", "", "Optional explicit local development origin")
	flag.BoolVar(&initialize, "initialize", false, "Validate and initialize the UI update source, then exit")
	flag.BoolVar(&checkOnly, "check", false, "Validate the managed configuration without applying it")
	flag.BoolVar(&reloadOnly, "reload-only", false, "Reload the existing config without changing it")
	flag.Parse()
	app, err := openApp(options)
	if err != nil {
		log.Fatal(err)
	}
	if checkOnly {
		data, _, err := compile(app.state)
		if err != nil {
			log.Fatal(err)
		}
		if err := app.validate(context.Background(), data); err != nil {
			log.Fatal(err)
		}
		log.Print("Mihomo configuration validated")
		return
	}
	if reloadOnly {
		if err := app.reload(context.Background()); err != nil {
			log.Fatal(err)
		}
		return
	}
	if initialize {
		if app.drift() {
			log.Fatal("设备配置已改变，请先采用当前配置")
		}
		if err := app.commit(context.Background(), clone(app.state)); err != nil {
			log.Fatal(err)
		}
		log.Print("Dashboard configuration initialized")
		return
	}
	listener, err := net.Listen("tcp4", listen)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Handler: app.handler(devOrigin), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 150 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	go app.scheduled(ctx)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("F50 configuration manager %s listening on %s", version, listen)
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
