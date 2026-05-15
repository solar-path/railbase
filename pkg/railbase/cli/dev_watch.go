package cli

// Phase 3.x — thin fsnotify shim for the dev command's
// --watch-schema feature. Wraps the package interface in a tiny
// adapter so dev.go's `watchSchemaAndRegen` doesn't import fsnotify
// directly (keeps the file size manageable + lets tests stub the
// watcher behind an interface in a follow-up if needed).
//
// Pure plumbing; no Railbase business logic lives here.

import "github.com/fsnotify/fsnotify"

type devWatcher struct {
	w *fsnotify.Watcher
}

func fsnotifyNew() (*devWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &devWatcher{w: w}, nil
}

func (d *devWatcher) Add(dir string) error    { return d.w.Add(dir) }
func (d *devWatcher) Close() error            { return d.w.Close() }
func (d *devWatcher) events() chan fsnotify.Event {
	return d.w.Events
}
func (d *devWatcher) errors() chan error {
	return d.w.Errors
}
