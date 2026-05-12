//go:build embed_pg

package embedded

import (
	"bytes"
	"io"
	"log/slog"
)

// newSlogWriter adapts the line-oriented io.Writer that
// embedded-postgres uses for its subprocess stdout/stderr into our
// structured slog logger. Each newline-terminated chunk becomes one
// log record at the requested level.
func newSlogWriter(log *slog.Logger, level slog.Level) io.Writer {
	return &slogWriter{log: log, level: level}
}

type slogWriter struct {
	log   *slog.Logger
	level slog.Level
	buf   bytes.Buffer
}

func (w *slogWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.buf.Write(p)
	for {
		idx := bytes.IndexByte(w.buf.Bytes(), '\n')
		if idx < 0 {
			break
		}
		line := w.buf.Next(idx + 1)
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			continue
		}
		w.log.Log(nil, w.level, "embedded-postgres", "line", string(line))
	}
	return n, nil
}
