package planindexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/josuebrunel/klodmem/internal/store"
)

const samplePlan = "# Plan: fix the build\n\n## Context\n\nThe build fails on CI because of a missing dependency.\n"

func writePlan(t *testing.T, root, filename, content string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	path := filepath.Join(root, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	return path
}

func newTestIndexer(t *testing.T) (*Indexer, *store.Store, string, context.Context) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "klodmem.db"))
	if err != nil {
		t.Fatalf("store.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return New(root, s, nil), s, root, ctx
}

func TestFullScanIndexesPlans(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	writePlan(t, root, "bug-md-glittery-raven.md", samplePlan)

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}

	results, err := s.SearchPlans(ctx, store.PlanSearchQuery{Query: "dependency"})
	if err != nil {
		t.Fatalf("SearchPlans() error: %v", err)
	}
	if len(results) != 1 || results[0].Name != "Plan: fix the build" {
		t.Fatalf("SearchPlans() = %+v, want one plan named %q", results, "Plan: fix the build")
	}
}

func TestFullScanPrunesDeletedPlans(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	path := writePlan(t, root, "note.md", samplePlan)

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}
	if _, ok, err := s.PlanMTime(ctx, path); err != nil || !ok {
		t.Fatalf("expected plan indexed before deletion, ok=%v err=%v", ok, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (after delete) error: %v", err)
	}
	if _, ok, err := s.PlanMTime(ctx, path); err != nil || ok {
		t.Fatalf("expected plan pruned after deletion, ok=%v err=%v", ok, err)
	}
	results, err := s.SearchPlans(ctx, store.PlanSearchQuery{Query: "dependency"})
	if err != nil {
		t.Fatalf("SearchPlans() error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("SearchPlans() after prune = %d results, want 0", len(results))
	}
}

func TestFullScanSkipsUnchangedPlans(t *testing.T) {
	idx, _, root, ctx := newTestIndexer(t)
	writePlan(t, root, "note.md", samplePlan)

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (second pass) error: %v", err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func TestWatchIndexesNewPlan(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	// Root exists so the watcher can be registered up front.
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- idx.Watch(watchCtx) }()

	time.Sleep(50 * time.Millisecond)
	writePlan(t, root, "new-one.md", "# Plan: fresh content\n\nFreshly discovered plan body.\n")

	waitFor(t, 3*time.Second, func() bool {
		results, err := s.SearchPlans(ctx, store.PlanSearchQuery{Query: "freshly"})
		return err == nil && len(results) == 1
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Watch() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch() did not return after context cancellation")
	}
}
