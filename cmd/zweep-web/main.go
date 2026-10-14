// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Command zweep-web is the web client plugin of Zweep: an optional, separate service that lets
// browsers (iPhone, desktop) receive the alarms. It talks to Zweep over the app protocol only.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/httpx"
	"github.com/n1k0droid/zweep/internal/webclient"
)

var version = "dev"

const usage = `zweep-web: the web client plugin of Zweep

Commands:
  serve         run the plugin (default)
  healthcheck   exit 0 if the plugin answers (container health checks)
  version       print the version

Configuration (environment):
  ZWEEP_WEB_ZWEEP_URL        public listener of Zweep as the plugin reaches it, e.g. http://zweep:8080 (required)
  ZWEEP_WEB_PUBLIC_URL       address the browsers use, https://… (required)
  ZWEEP_WEB_KEY_FILE         file with the 32-byte key that encrypts the state (required; raw or base64)
  ZWEEP_WEB_DATA_DIR         where the state lives (default /var/lib/zweep-web)
  ZWEEP_WEB_LISTEN           listen address (default :8090)
  ZWEEP_WEB_TRUSTED_PROXIES  comma-separated IPs or CIDRs of the reverse proxies in front of the plugin
  ZWEEP_WEB_CLIENT_ALLOWED_IPS
                             optional: IPs or CIDRs the web client may be used from (fixed addresses, remote
                             sites, a VPN); empty: from anywhere
  ZWEEP_WEB_CONTACT          mailto: or https: address shown to the push services (default: the project page)
  ZWEEP_WEB_LOG_LEVEL        debug, info, warn or error (default info)
`

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "healthcheck":
		err = healthcheck()
	case "version":
		fmt.Println("zweep-web", version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func prefixes(list string) (httpx.Proxies, error) {
	var out httpx.Proxies
	for _, s := range strings.Split(list, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		} else {
			return nil, fmt.Errorf("invalid address %q", s)
		}
	}
	return out, nil
}

func serve() error {
	var level slog.Level
	if err := level.UnmarshalText([]byte(env("ZWEEP_WEB_LOG_LEVEL", "info"))); err != nil {
		return errors.New("ZWEEP_WEB_LOG_LEVEL must be debug, info, warn or error")
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	keyFile := env("ZWEEP_WEB_KEY_FILE", "")
	if keyFile == "" {
		return errors.New("ZWEEP_WEB_KEY_FILE is required (32 bytes, raw or base64)")
	}
	key, err := crypto.LoadKey(keyFile)
	if err != nil {
		return err
	}
	proxies, err := prefixes(env("ZWEEP_WEB_TRUSTED_PROXIES", ""))
	if err != nil {
		return fmt.Errorf("ZWEEP_WEB_TRUSTED_PROXIES: %w", err)
	}
	grace, err := time.ParseDuration(env("ZWEEP_WEB_CODE_GRACE", "5m"))
	if err != nil || grace < 0 || grace > time.Hour {
		return errors.New("ZWEEP_WEB_CODE_GRACE must be a duration between 0 and 1h")
	}
	allowed, err := prefixes(env("ZWEEP_WEB_CLIENT_ALLOWED_IPS", ""))
	if err != nil {
		return fmt.Errorf("ZWEEP_WEB_CLIENT_ALLOWED_IPS: %w", err)
	}
	svc, err := webclient.New(webclient.Config{
		ClientAllowed: allowed,
		ZweepURL:      env("ZWEEP_WEB_ZWEEP_URL", ""), PublicURL: env("ZWEEP_WEB_PUBLIC_URL", ""),
		DataDir: env("ZWEEP_WEB_DATA_DIR", "/var/lib/zweep-web"), Key: key,
		Contact: env("ZWEEP_WEB_CONTACT", "https://github.com/N1k0droid/zweep"), Proxies: proxies, Version: version, CodeGrace: grace,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go svc.Run(ctx)

	addr := env("ZWEEP_WEB_LISTEN", ":8090")
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler: svc.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second,
		IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
	}
	if u := env("ZWEEP_WEB_PUBLIC_URL", ""); !strings.HasPrefix(u, "https://") {
		slog.Warn("The public URL is not https: session cookies travel in the clear and browsers refuse notifications outside localhost", "component", "webclient")
	}
	slog.Info("Zweep web client plugin listening", "component", "webclient", "version", version, "addr", ln.Addr().String())
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

func healthcheck() error {
	addr := env("ZWEEP_WEB_LISTEN", ":8090")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := &http.Client{Timeout: 4 * time.Second}
	res, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unhealthy: HTTP %d", res.StatusCode)
	}
	return nil
}
