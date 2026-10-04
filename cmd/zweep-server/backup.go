// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/n1k0droid/zweep/internal/backup"
	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/store"
)

// backupFlags reads the database URL and the master key, as the server does
func backupFlags(name string, args []string, getenv func(string) string, stderr io.Writer, extra func(*flag.FlagSet)) (string, *crypto.Box, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyFile := fs.String("master-key-file", "", "file with the master key (env ZWEEP_MASTER_KEY_FILE)")
	extra(fs)
	dbURL, err := config.LoadDatabaseURL(fs, args, getenv)
	if err != nil {
		return "", nil, err
	}
	if *keyFile == "" {
		*keyFile = getenv("ZWEEP_MASTER_KEY_FILE")
	}
	if *keyFile == "" {
		return "", nil, errors.New("master-key-file is required: the backup is encrypted with the master key")
	}
	key, err := crypto.LoadKey(*keyFile)
	if err != nil {
		return "", nil, err
	}
	box, err := crypto.NewBox(key)
	return dbURL, box, err
}

// backupCmd writes an encrypted backup of the database to a file (or "-" for the standard output)
func backupCmd(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	var out string
	dbURL, box, err := backupFlags("backup", args, getenv, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&out, "out", "", `backup file to write ("-": standard output); default zweep-backup-<UTC time>.zwbk`)
	})
	if err != nil {
		return err
	}
	if out == "" {
		out = "zweep-backup-" + time.Now().UTC().Format("20060102-150405") + ".zwbk"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	var w io.Writer = stdout
	var tmp string
	if out != "-" {
		tmp = out + ".partial"
		f, err := os.OpenFile(filepath.Clean(tmp), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		w = f
	}
	m, err := backup.Write(ctx, st, box, version, w)
	if err != nil {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
		return err
	}
	if tmp != "" {
		if f, ok := w.(*os.File); ok {
			if err := f.Sync(); err != nil {
				return err
			}
			_ = f.Close()
		}
		if err := os.Rename(tmp, out); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "backup written: %s (schema %d, %d tables)\n", out, m.Schema, len(m.Tables))
	}
	_ = st.Audit(ctx, store.AuditEntry{ActorType: store.ActorCLI, Action: "backup.created", Target: filepath.Base(out), Outcome: "ok",
		Details: map[string]any{"schema": m.Schema, "tables": len(m.Tables)}})
	return nil
}

// restoreCmd loads a backup into an empty database
func restoreCmd(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	var in string
	var check bool
	dbURL, box, err := backupFlags("restore", args, getenv, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&in, "in", "", "backup file to restore")
		fs.BoolVar(&check, "check", false, "only check the file and the master key, print what it contains")
	})
	if err != nil {
		return err
	}
	if in == "" {
		return errors.New("-in is required")
	}
	f, err := os.Open(filepath.Clean(in))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if check {
		m, err := backup.Inspect(f, box)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Zweep backup of %s (server %s, Zweep %s, schema %d, %d tables)\n",
			m.CreatedAt.Format(time.RFC3339), m.ServerID, m.Zweep, m.Schema, len(m.Tables))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	m, err := backup.Restore(ctx, st, box, f)
	if err != nil {
		return err
	}
	_ = st.Audit(ctx, store.AuditEntry{ActorType: store.ActorCLI, Action: "backup.restored", Target: filepath.Base(in), Outcome: "ok",
		Details: map[string]any{"created_at": m.CreatedAt, "schema": m.Schema, "zweep": m.Zweep}})
	_, _ = fmt.Fprintf(stdout, "restored the backup of %s (server %s); start the server on this database\n", m.CreatedAt.Format(time.RFC3339), m.ServerID)
	return nil
}
