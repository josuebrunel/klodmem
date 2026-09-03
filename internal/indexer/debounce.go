package indexer

import (
	"sync"
	"time"
)

// debouncer delays calling fn(path) until no further trigger(path) calls
// arrive for a given path within window, collapsing rapid successive writes
// to the same file into a single reindex.
type debouncer struct {
	window time.Duration
	fn     func(path string)

	mu     sync.Mutex
	timers map[string]*time.Timer
	closed bool
}

func newDebouncer(window time.Duration, fn func(path string)) *debouncer {
	return &debouncer{
		window: window,
		fn:     fn,
		timers: make(map[string]*time.Timer),
	}
}

func (d *debouncer) trigger(path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}

	if t, ok := d.timers[path]; ok {
		t.Stop()
	}
	d.timers[path] = time.AfterFunc(d.window, func() {
		d.fn(path)
		d.mu.Lock()
		delete(d.timers, path)
		d.mu.Unlock()
	})
}

func (d *debouncer) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	for _, t := range d.timers {
		t.Stop()
	}
}
