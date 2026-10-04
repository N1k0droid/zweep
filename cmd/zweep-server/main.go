// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Command zweep-server runs the Zweep service ("Zweep for Zabbix").
//
// Made by N1k0droid (https://github.com/N1k0droid). Like Zweep? A star on github.com/N1k0droid/zweep and a
// follow help the project grow.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/n1k0droid/zweep/internal/auth"
	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/logbuf"
	"github.com/n1k0droid/zweep/internal/server"
	"github.com/n1k0droid/zweep/internal/store"
)

// version is set at build time (-ldflags "-X main.version=…")
var version = "dev"

const usage = `Usage: zweep-server <command> [options]

Commands:
  serve                     run the service (default)
  healthcheck [-url URL]    exit 0 if the service is healthy (container health checks)
  admin bootstrap -username NAME
                            create the first admin; the password is read from stdin
  admin reset-password -username NAME
                            set a new password for a user; the password is read from stdin
  admin reset-totp -username NAME
                            remove the two-step verification of a user (lost authenticator)
  backup [-out FILE]        write an encrypted backup of the database (needs the master key)
  restore -in FILE [-check] restore a backup into an empty database (-check: verify file and key only)
  version                   print the version

Run "zweep-server serve -h" for the service options (each also as a ZWEEP_* variable).
`

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(args, getenv, stderr)
	case "healthcheck":
		err = healthcheck(args, getenv, stderr)
	case "admin":
		err = admin(args, getenv, stdin, stdout, stderr)
	case "backup":
		err = backupCmd(args, getenv, stdout, stderr)
	case "restore":
		err = restoreCmd(args, getenv, stdout, stderr)
	case "version":
		_, _ = fmt.Fprintln(stdout, "zweep-server", version)
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, usage)
	default:
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintln(stderr, "error:", err)
		}
		return 1
	}
	return 0
}

func serve(args []string, getenv func(string) string, stderr io.Writer) error {
	cfg, err := config.Load(args, getenv, stderr)
	if err != nil {
		return err
	}
	cfg.Version = version
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var h slog.Handler = slog.NewJSONHandler(os.Stderr, opts)
	if cfg.LogFormat == "text" {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	// A copy of the latest records stays in memory for the Logging page of the dashboard
	slog.SetDefault(slog.New(logbuf.NewHandler(h, logbuf.Default)).With("node", cfg.NodeID))
	slog.Info("Starting Zweep", "component", "server", "version", version)
	for _, w := range cfg.Warnings {
		slog.Warn("Configuration warning", "component", "config", "detail", w)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, err := server.New(cfg)
	if err != nil {
		return err
	}
	return s.Run(ctx)
}

func healthcheck(args []string, getenv func(string) string, stderr io.Writer) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	def := getenv("ZWEEP_HEALTHCHECK_URL")
	if def == "" {
		def = "http://127.0.0.1:8080/v1/health"
	}
	u := fs.String("url", def, "health URL (env ZWEEP_HEALTHCHECK_URL)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(*u) // #nosec G107 -- URL configured by the operator
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: HTTP %d", resp.StatusCode)
	}
	return nil
}

// admin holds the break-glass commands: they work without the admin API and are audited
func admin(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "bootstrap" && args[0] != "reset-password" && args[0] != "reset-totp") {
		return errors.New(`admin: expected "bootstrap", "reset-password" or "reset-totp"`)
	}
	sub := args[0]
	fs := flag.NewFlagSet("admin "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	username := fs.String("username", "", "username")
	dbURL, err := config.LoadDatabaseURL(fs, args[1:], getenv)
	if err != nil {
		return err
	}
	if !store.ValidUsername(*username) {
		return errors.New("a valid -username is required")
	}
	var password string
	if sub != "reset-totp" { // the only command without a new password
		if password, err = readPassword(stdin); err != nil {
			return err
		}
		if err := auth.CheckPolicy(*username, password); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	var hash string
	if sub != "reset-totp" {
		if hash, err = auth.Hash(ctx, password); err != nil {
			return err
		}
	}
	switch sub {
	case "bootstrap":
		n, err := st.CountAdmins(ctx, false)
		if err != nil {
			return err
		}
		if n > 0 {
			return errors.New("an admin already exists: use the admin API, or reset-password to recover access")
		}
		if err := st.CreateUser(ctx, &store.User{Username: *username, Role: store.RoleAdmin, PasswordHash: hash}, store.ActorCLI); err != nil {
			return err
		}
	case "reset-password":
		if err := st.SetPasswordHash(ctx, *username, hash, store.ActorCLI); err != nil {
			return err
		}
	case "reset-totp":
		// Last resort when the authenticator is lost and nobody can reset it from the dashboard
		// (e.g. the primary admin): whoever runs this already controls the server
		if _, err := st.ResetTOTP(ctx, *username, store.ActorCLI); err != nil {
			return err
		}
	}
	if err := st.Audit(ctx, store.AuditEntry{ActorType: store.ActorCLI, Action: "cli.admin." + sub, Target: *username, Outcome: "ok"}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "%s: done for %q\n", sub, *username)
	return nil
}

// readPassword reads one line from stdin (piped or typed); it is never taken from arguments or
// the environment, which leak into shell history and process listings
func readPassword(stdin io.Reader) (string, error) {
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("the password must be given on stdin")
	}
	return line, nil
}
