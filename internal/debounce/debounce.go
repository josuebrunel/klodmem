// Package debounce collapses rapid repeated triggers for the same key into a
// single delayed call, e.g. for reacting to fsnotify events without
// reprocessing a file on every write syscall.
package debounce

import (
	"sync"
	"time"
)

// Debouncer delays calling fn(key) until no further Trigger(key) calls
// arrive for that key within window.
type Debouncer struct {
	window time.Duration
	fn     func(key string)

	mu     sync.Mutex
	timers map[string]*time.Timer
	closed bool
}

// New creates a Debouncer that calls fn(key) window after the most recent
// Trigger(key) call for that key.
func New(window time.Duration, fn func(key string)) *Debouncer {
	return &Debouncer{
		window: window,
		fn:     fn,
		timers: make(map[string]*time.Timer),
	}
}

// Trigger (re)starts the delay timer for key.
func (d *Debouncer) Trigger(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}

	if t, ok := d.timers[key]; ok {
		t.Stop()
	}
	d.timers[key] = time.AfterFunc(d.window, func() {
		d.fn(key)
		d.mu.Lock()
		delete(d.timers, key)
		d.mu.Unlock()
	})
}

// Stop cancels all pending timers; no further calls to fn will occur.
func (d *Debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	for _, t := range d.timers {
		t.Stop()
	}
}
