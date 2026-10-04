// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package backup writes and restores a consistent copy of the Zweep database (phase 8). The file is
// compressed and encrypted with the master key (it holds personal data: users, audit trail, alarms):
// without the master key it cannot be read, and the master key is needed anyway to use the secrets
// stored in the database. Keep the master key apart from the backups.
package backup

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/store"
)

// magic starts every backup file
const magic = "ZWEEPBK1\n"

// chunkSize is the plaintext size of a sealed chunk
const chunkSize = 1 << 20

// Manifest describes a backup
type Manifest struct {
	Format    string    `json:"format"` // "zweep-backup"
	Version   int       `json:"version"`
	Schema    int       `json:"schema"` // schema version of the database
	ServerID  string    `json:"server_id"`
	Zweep     string    `json:"zweep"` // version of the server that wrote it
	CreatedAt time.Time `json:"created_at"`
	Tables    []string  `json:"tables"`
}

// skipped tables: the schema version is written by the restore, sessions are not worth keeping
var skipped = []string{"zw_schema_version", "zw_admin_session"}

// Errors of a restore
var (
	ErrNotBackup    = errors.New("not a Zweep backup file")
	ErrWrongKey     = errors.New("cannot decrypt the backup: wrong master key, or damaged file")
	ErrTruncated    = errors.New("the backup file is truncated")
	ErrNotEmpty     = errors.New("the target database is not empty: restore into a new, empty database")
	ErrSchemaTooNew = errors.New("the backup comes from a newer Zweep: update this server first")
	endOfTable      = []byte("\\.\n")
	tablePrefix     = "TABLE "
	unknownTables   = "the backup contains tables unknown to this server: "
)

// Write takes a consistent snapshot of the database (one repeatable-read transaction) and writes it,
// compressed and sealed, to w. The server may keep running meanwhile.
func Write(ctx context.Context, st *store.Store, box *crypto.Box, zweepVersion string, w io.Writer) (*Manifest, error) {
	if _, err := io.WriteString(w, magic); err != nil {
		return nil, err
	}
	sw := &sealedWriter{w: w, box: box}
	gz := gzip.NewWriter(sw)
	var m *Manifest
	err := pgx.BeginTxFunc(ctx, st.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		tables, err := tablesOf(ctx, tx)
		if err != nil {
			return err
		}
		var schema int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(version), 0) FROM zw_schema_version`).Scan(&schema); err != nil {
			return err
		}
		var serverID string
		_ = tx.QueryRow(ctx, `SELECT value #>> '{}' FROM zw_setting WHERE key = 'server.id'`).Scan(&serverID)
		m = &Manifest{Format: "zweep-backup", Version: 1, Schema: schema, ServerID: serverID, Zweep: zweepVersion,
			CreatedAt: time.Now().UTC(), Tables: tables}
		head, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if _, err := gz.Write(append(head, '\n')); err != nil {
			return err
		}
		for _, t := range tables {
			if _, err := io.WriteString(gz, tablePrefix+t+"\n"); err != nil {
				return err
			}
			if _, err := tx.Conn().PgConn().CopyTo(ctx, gz, "COPY "+pgx.Identifier{t}.Sanitize()+" TO STDOUT"); err != nil {
				return fmt.Errorf("table %s: %w", t, err)
			}
			if _, err := gz.Write(endOfTable); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if err := sw.Close(); err != nil {
		return nil, err
	}
	return m, nil
}

// tablesOf lists the Zweep tables of the current schema
func tablesOf(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' AND table_name LIKE 'zw\_%' ORDER BY table_name`)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(names, func(n string) bool { return slices.Contains(skipped, n) }), nil
}

// Inspect reads the manifest of a backup (and checks the key) without restoring it
func Inspect(r io.Reader, box *crypto.Box) (*Manifest, error) {
	m, _, err := open(r, box)
	return m, err
}

func open(r io.Reader, box *crypto.Box) (*Manifest, *bufio.Reader, error) {
	head := make([]byte, len(magic))
	if _, err := io.ReadFull(r, head); err != nil || string(head) != magic {
		return nil, nil, ErrNotBackup
	}
	gz, err := gzip.NewReader(&sealedReader{r: r, box: box})
	if err != nil {
		if errors.Is(err, ErrWrongKey) || errors.Is(err, ErrTruncated) {
			return nil, nil, err
		}
		return nil, nil, ErrNotBackup
	}
	br := bufio.NewReaderSize(gz, 64<<10)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return nil, nil, wrapRead(err)
	}
	var m Manifest
	if err := json.Unmarshal(line, &m); err != nil || m.Format != "zweep-backup" {
		return nil, nil, ErrNotBackup
	}
	return &m, br, nil
}

func wrapRead(err error) error {
	if errors.Is(err, ErrWrongKey) || errors.Is(err, ErrTruncated) {
		return err
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return ErrTruncated
	}
	return err
}

// Restore loads a backup into an empty database: the schema of the backup is created, the data
// loaded in one transaction, then the schema is brought to the version of this server.
func Restore(ctx context.Context, st *store.Store, box *crypto.Box, r io.Reader) (*Manifest, error) {
	m, br, err := open(r, box)
	if err != nil {
		return nil, err
	}
	if m.Schema > store.SchemaVersion() {
		return nil, ErrSchemaTooNew
	}
	var existing int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name LIKE 'zw\_%'`).Scan(&existing); err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, ErrNotEmpty
	}
	if err := st.MigrateTo(ctx, m.Schema); err != nil {
		return nil, err
	}
	err = pgx.BeginFunc(ctx, st.Pool, func(tx pgx.Tx) error {
		known, err := tablesOf(ctx, tx)
		if err != nil {
			return err
		}
		var unknown []string
		for _, t := range m.Tables {
			if !slices.Contains(known, t) {
				unknown = append(unknown, t)
			}
		}
		if len(unknown) > 0 {
			return errors.New(unknownTables + strings.Join(unknown, ", "))
		}
		// Foreign keys: the data is checked at commit, whatever the order of the tables in the file
		if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL DEFERRED`); err != nil {
			return err
		}
		if err := deferForeignKeys(ctx, tx); err != nil {
			return err
		}
		for range m.Tables {
			line, err := br.ReadString('\n')
			if err != nil {
				return wrapRead(err)
			}
			t, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), tablePrefix)
			if !ok || !slices.Contains(m.Tables, t) {
				return ErrNotBackup
			}
			// The settings created by the schema (server id) are replaced by those of the backup
			if _, err := tx.Exec(ctx, "DELETE FROM "+pgx.Identifier{t}.Sanitize()); err != nil {
				return err
			}
			if _, err := tx.Conn().PgConn().CopyFrom(ctx, &tableReader{br: br}, "COPY "+pgx.Identifier{t}.Sanitize()+" FROM STDIN"); err != nil {
				return fmt.Errorf("table %s: %w", t, wrapRead(err))
			}
		}
		return resetSequences(ctx, tx)
	})
	if err != nil {
		return nil, err
	}
	return m, st.Migrate(ctx)
}

// deferForeignKeys makes the foreign keys of the Zweep tables deferrable for this transaction only:
// they are restored as they were by the schema migrations that follow (ALTER is transactional)
func deferForeignKeys(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT c.conrelid::regclass::text, c.conname FROM pg_constraint c
		JOIN pg_namespace n ON n.oid = c.connamespace
		WHERE c.contype = 'f' AND n.nspname = current_schema() AND NOT c.condeferrable`)
	if err != nil {
		return err
	}
	type fk struct{ table, name string }
	fks, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (fk, error) {
		var f fk
		err := r.Scan(&f.table, &f.name)
		return f, err
	})
	if err != nil {
		return err
	}
	for _, f := range fks {
		if _, err := tx.Exec(ctx, "ALTER TABLE "+f.table+" ALTER CONSTRAINT "+pgx.Identifier{f.name}.Sanitize()+" DEFERRABLE INITIALLY DEFERRED"); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL DEFERRED`); err != nil {
		return err
	}
	// Back to immediate at the end of the transaction: re-declared before commit
	_, err = tx.Exec(ctx, `CREATE TEMP TABLE zw_restore_fks (tbl text, name text) ON COMMIT DROP`)
	if err != nil {
		return err
	}
	for _, f := range fks {
		if _, err := tx.Exec(ctx, `INSERT INTO zw_restore_fks VALUES ($1, $2)`, f.table, f.name); err != nil {
			return err
		}
	}
	return nil
}

// resetSequences moves every sequence after the restored rows, then gives the foreign keys back their
// original (not deferrable) form
func resetSequences(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name LIKE 'zw\_%' AND (column_default LIKE 'nextval%' OR is_identity = 'YES')`)
	if err != nil {
		return err
	}
	type col struct{ table, column string }
	cols, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (col, error) {
		var c col
		err := r.Scan(&c.table, &c.column)
		return c, err
	})
	if err != nil {
		return err
	}
	for _, c := range cols {
		t, cname := pgx.Identifier{c.table}.Sanitize(), pgx.Identifier{c.column}.Sanitize()
		if _, err := tx.Exec(ctx, `SELECT setval(pg_get_serial_sequence($1, $2), COALESCE((SELECT max(`+cname+`) FROM `+t+`), 0) + 1, false)`,
			c.table, c.column); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return err // a foreign key broken by the data: nothing is committed
	}
	fkRows, err := tx.Query(ctx, `SELECT tbl, name FROM zw_restore_fks`)
	if err != nil {
		return err
	}
	type fk struct{ table, name string }
	fks, err := pgx.CollectRows(fkRows, func(r pgx.CollectableRow) (fk, error) {
		var f fk
		err := r.Scan(&f.table, &f.name)
		return f, err
	})
	if err != nil {
		return err
	}
	for _, f := range fks {
		if _, err := tx.Exec(ctx, "ALTER TABLE "+f.table+" ALTER CONSTRAINT "+pgx.Identifier{f.name}.Sanitize()+" NOT DEFERRABLE"); err != nil {
			return err
		}
	}
	return nil
}

// tableReader returns the COPY data of one table, up to its end marker
type tableReader struct {
	br   *bufio.Reader
	done bool
	buf  []byte
}

func (t *tableReader) Read(p []byte) (int, error) {
	for len(t.buf) == 0 {
		if t.done {
			return 0, io.EOF
		}
		line, err := t.br.ReadBytes('\n')
		if err != nil {
			return 0, wrapRead(err)
		}
		if string(line) == string(endOfTable) {
			t.done = true
			continue
		}
		t.buf = line
	}
	n := copy(p, t.buf)
	t.buf = t.buf[n:]
	return n, nil
}

// ---- sealed chunks ----

// sealedWriter seals the stream in chunks: [4-byte length][sealed(8-byte index, 1-byte last, data)].
// The index and the last flag are authenticated: chunks cannot be reordered, dropped or cut off.
type sealedWriter struct {
	w     io.Writer
	box   *crypto.Box
	buf   []byte
	index uint64
}

func (s *sealedWriter) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for len(s.buf) >= chunkSize {
		if err := s.flush(s.buf[:chunkSize], false); err != nil {
			return 0, err
		}
		s.buf = s.buf[chunkSize:]
	}
	return len(p), nil
}

func (s *sealedWriter) flush(data []byte, last bool) error {
	plain := make([]byte, 9, 9+len(data))
	binary.BigEndian.PutUint64(plain, s.index)
	if last {
		plain[8] = 1
	}
	plain = append(plain, data...)
	sealed, err := s.box.Seal(plain)
	if err != nil {
		return err
	}
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(sealed))) // #nosec G115 -- a chunk is about 1 MiB
	if _, err := s.w.Write(n[:]); err != nil {
		return err
	}
	_, err = s.w.Write(sealed)
	s.index++
	return err
}

// Close writes the last chunk
func (s *sealedWriter) Close() error {
	err := s.flush(s.buf, true)
	s.buf = nil
	return err
}

type sealedReader struct {
	r     io.Reader
	box   *crypto.Box
	buf   []byte
	index uint64
	last  bool
}

func (s *sealedReader) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		if s.last {
			return 0, io.EOF
		}
		var n [4]byte
		if _, err := io.ReadFull(s.r, n[:]); err != nil {
			return 0, ErrTruncated
		}
		size := binary.BigEndian.Uint32(n[:])
		if size > 2*chunkSize {
			return 0, ErrNotBackup
		}
		sealed := make([]byte, size)
		if _, err := io.ReadFull(s.r, sealed); err != nil {
			return 0, ErrTruncated
		}
		plain, err := s.box.Open(sealed)
		if err != nil || len(plain) < 9 {
			return 0, ErrWrongKey
		}
		if binary.BigEndian.Uint64(plain) != s.index {
			return 0, ErrWrongKey // reordered or replaced chunk
		}
		s.index++
		s.last = plain[8] == 1
		s.buf = plain[9:]
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}
