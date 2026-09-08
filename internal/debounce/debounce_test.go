package debounce

import (
	"sync"
	"testing"
	"time"
)

const testWindow = 30 * time.Millisecond

func TestTriggerCollapsesRapidCalls(t *testing.T) {
	var mu sync.Mutex
	var calls []string

	d := New(testWindow, func(key string) {
		mu.Lock()
		calls = append(calls, key)
		mu.Unlock()
	})
	t.Cleanup(d.Stop)

	for i := 0; i < 5; i++ {
		d.Trigger("a")
		time.Sleep(testWindow / 4)
	}

	time.Sleep(2 * testWindow)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("calls = %v, want exactly one call after rapid triggers collapse", calls)
	}
	if calls[0] != "a" {
		t.Fatalf("calls[0] = %q, want %q", calls[0], "a")
	}
}

func TestTriggerDistinctKeysCallIndependently(t *testing.T) {
	var mu sync.Mutex
	seen := make(map[string]int)

	d := New(testWindow, func(key string) {
		mu.Lock()
		seen[key]++
		mu.Unlock()
	})
	t.Cleanup(d.Stop)

	d.Trigger("a")
	d.Trigger("b")

	time.Sleep(2 * testWindow)

	mu.Lock()
	defer mu.Unlock()
	if seen["a"] != 1 || seen["b"] != 1 {
		t.Fatalf("seen = %v, want a:1 b:1", seen)
	}
}

func TestStopPreventsPendingCall(t *testing.T) {
	var mu sync.Mutex
	called := false

	d := New(testWindow, func(string) {
		mu.Lock()
		called = true
		mu.Unlock()
	})

	d.Trigger("a")
	d.Stop()

	time.Sleep(2 * testWindow)

	mu.Lock()
	defer mu.Unlock()
	if called {
		t.Fatalf("fn was called after Stop(), want it suppressed")
	}
}

func TestTriggerAfterStopIsNoop(t *testing.T) {
	var mu sync.Mutex
	called := false

	d := New(testWindow, func(string) {
		mu.Lock()
		called = true
		mu.Unlock()
	})
	d.Stop()

	d.Trigger("a")
	time.Sleep(2 * testWindow)

	mu.Lock()
	defer mu.Unlock()
	if called {
		t.Fatalf("fn was called from Trigger() after Stop(), want no-op")
	}
}
