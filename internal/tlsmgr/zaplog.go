// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package tlsmgr

import (
	"context"
	"log/slog"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// zapLogger sends the logs of CertMagic (zap) to the Zweep logger (slog, component "tls"): they reach
// the standard output and the Logging page like every other line. Debug lines are dropped.
func zapLogger() *zap.Logger { return zap.New(slogCore{level: zapcore.InfoLevel}) }

type slogCore struct {
	level  zapcore.Level
	fields []zapcore.Field
}

func (c slogCore) Enabled(l zapcore.Level) bool { return l >= c.level }

func (c slogCore) With(fields []zapcore.Field) zapcore.Core {
	return slogCore{level: c.level, fields: append(append([]zapcore.Field{}, c.fields...), fields...)}
}

func (c slogCore) Check(e zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(e.Level) {
		return ce.AddCore(e, c)
	}
	return ce
}

func (c slogCore) Write(e zapcore.Entry, fields []zapcore.Field) error {
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range append(append([]zapcore.Field{}, c.fields...), fields...) {
		f.AddTo(enc)
	}
	attrs := []any{"component", "tls"}
	if e.LoggerName != "" {
		attrs = append(attrs, "logger", e.LoggerName)
	}
	for k, v := range enc.Fields {
		attrs = append(attrs, k, v)
	}
	level := slog.LevelInfo
	switch {
	case e.Level >= zapcore.ErrorLevel:
		level = slog.LevelError
	case e.Level == zapcore.WarnLevel:
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level, e.Message, attrs...)
	return nil
}

func (c slogCore) Sync() error { return nil }
