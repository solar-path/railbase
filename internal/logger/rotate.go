// Date-rotating writer for slog. Cuts a new file at local-midnight,
// retiring files past the retention window. NOT size-based — date-only
// rotation matches the operator pattern "give me yesterday's logs"
// which is the common reason an operator opens log files at all.
//
// Why not lumberjack: lumberjack is great for size+age, but its rotation
// trigger is "lines written exceeds N bytes" which produces unaligned
// boundaries ("the 23:58 line is in today.1.log, the 00:01 line is in
// today.log") and forces operators to grep across files for one day's
// view. A date-cut writer keeps "today.log = today's lines" simple.

package logger

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// dateRotatingWriter is an io.Writer that appends each Write to a file
// named `<dir>/railbase-YYYY-MM-DD.log`, switching to a new file when
// the local date rolls over. Optional retention purges older files.
//
// All Writes are guarded by a mutex so the slog Handler (which may
// fire from many goroutines) sees serialised I/O. A line-level mutex
// is sufficient — slog records arrive pre-formatted with a trailing
// newline, so we never need to split a write.
//
// Zero value is invalid; use newDateRotatingWriter.
type dateRotatingWriter struct {
	dir       string
	prefix    string // e.g. "railbase-"
	retention time.Duration
	clock     func() time.Time

	mu        sync.Mutex
	file      *os.File
	curDate   string // "YYYY-MM-DD" of the open file
	lastSweep time.Time
}

// newDateRotatingWriter constructs the writer. Creates `dir` if missing
// (mode 0o755). Retention=0 disables purging — files accumulate forever
// and the operator manages cleanup themselves. Negative retention is
// treated as 0.
//
// The first Write opens the file lazily, so a logger constructed but
// never used produces no I/O — handy for tests that build an App but
// never call Run.
func newDateRotatingWriter(dir string, retention time.Duration) (*dateRotatingWriter, error) {
	if dir == "" {
		return nil, errors.New("logger: rotate dir is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if retention < 0 {
		retention = 0
	}
	return &dateRotatingWriter{
		dir:       dir,
		prefix:    "railbase-",
		retention: retention,
		clock:     time.Now,
	}, nil
}

// Write implements io.Writer. On the first call (or when the local date
// has rolled), opens / rotates the destination file BEFORE writing. We
// do not buffer — slog already batches records into one Write per
// record, and an interactive operator running `tail -f` wants immediate
// visibility.
func (w *dateRotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	today := w.clock().Format("2006-01-02")
	if w.file == nil || w.curDate != today {
		if err := w.rotateLocked(today); err != nil {
			return 0, err
		}
	}
	return w.file.Write(p)
}

// rotateLocked closes the current file (if any), opens the file for
// `date`, and best-effort purges files older than the retention window.
// Called with w.mu held.
func (w *dateRotatingWriter) rotateLocked(date string) error {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	path := filepath.Join(w.dir, w.prefix+date+".log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	w.file = f
	w.curDate = date

	// Retention sweep at most once an hour — cheap enough but no point
	// scanning the dir on every single Write.
	if w.retention > 0 && w.clock().Sub(w.lastSweep) > time.Hour {
		w.sweepLocked()
		w.lastSweep = w.clock()
	}
	return nil
}

// sweepLocked deletes files matching `railbase-YYYY-MM-DD.log` whose
// date is older than `now - retention`. Best-effort — any error is
// swallowed (we'd be logging into the very file we're trying to manage,
// which would deadlock the writer's mutex).
func (w *dateRotatingWriter) sweepLocked() {
	cutoff := w.clock().Add(-w.retention)
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	dateRe := regexp.MustCompile(`^railbase-(\d{4}-\d{2}-\d{2})\.log$`)
	var stale []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := dateRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02", m[1], time.Local)
		if err != nil {
			continue
		}
		// `t` is midnight of that date; add 24h so a file dated
		// 2026-05-01 is "stale" only after 2026-05-02 00:00.
		if t.Add(24 * time.Hour).Before(cutoff) {
			stale = append(stale, filepath.Join(w.dir, e.Name()))
		}
	}
	sort.Strings(stale) // deterministic for tests
	for _, p := range stale {
		_ = os.Remove(p)
	}
}

// Close releases the open file. Safe to call multiple times.
func (w *dateRotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// Make sure dateRotatingWriter satisfies io.WriteCloser.
var _ io.WriteCloser = (*dateRotatingWriter)(nil)

// CurrentLogPath returns the canonical path for "today's" log file under
// dir. Used by the startup banner so the operator sees `tail -f` target
// printed once at boot. Returns "" if dir is empty.
func CurrentLogPath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "railbase-"+time.Now().Format("2006-01-02")+".log")
}

// IsLogFile checks if filename matches the date-rotated log file shape.
// Helpful for any future admin-UI surface that wants to list available
// log files for download.
func IsLogFile(name string) bool {
	return strings.HasPrefix(name, "railbase-") && strings.HasSuffix(name, ".log")
}
