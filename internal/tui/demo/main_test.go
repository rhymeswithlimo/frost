package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/manifest"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/storage/storagetest"
)

func TestBuildHistory(t *testing.T) {
	ctx := context.Background()
	key, err := crypto.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	mem := storagetest.NewMem()
	r, err := repo.Init(ctx, mem, key)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Open(filepath.Join(t.TempDir(), "manifest.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	e := &engine.Engine{Repo: r, Manifest: m}
	before := time.Now()
	if err := buildHistory(ctx, e, filepath.Join(t.TempDir(), "home")); err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	snaps, err := r.Snapshots(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 8 {
		t.Fatalf("got %d snapshots, want 8", len(snaps))
	}
	sortByTime(snaps)
	for i, s := range snaps {
		if !s.Time.Before(before) {
			t.Fatalf("snapshot %s wasn't backdated", s.ID)
		}
		if s.Host != demoHost {
			t.Fatalf("snapshot %s names the computer %q, want the demo's %q", s.ID, s.Host, demoHost)
		}
		if i > 0 && !s.Time.After(snaps[i-1].Time) {
			t.Fatal("history isn't chronological")
		}
		cached, ok := m.Snapshots()[s.ID]
		if !ok || !cached.Time.Equal(s.Time) {
			t.Fatal("cached timestamp differs from stored header")
		}
		tree, err := r.LoadTree(ctx, s.ID)
		if err != nil || len(tree.Files) == 0 {
			t.Fatalf("snapshot tree: %v", err)
		}
		if _, err := r.SaveSnapshot(ctx, s, tree, nil); err == nil {
			t.Fatal("committed snapshot overwrite was allowed")
		}
	}
	if snaps[0].Time.Before(before.Add(-14*24*time.Hour)) || snaps[0].Time.After(after.Add(-14*24*time.Hour)) {
		t.Fatal("oldest snapshot has wrong age")
	}
	if snaps[7].Time.Before(before.Add(-3*time.Hour)) || snaps[7].Time.After(after.Add(-3*time.Hour)) {
		t.Fatal("newest snapshot has wrong age")
	}
	v, err := e.Verify(ctx, m.ChunkCount(), false)
	if err != nil || !v.OK() {
		t.Fatalf("demo verification: %+v, %v", v, err)
	}
	if _, err := e.Restore(ctx, snaps[7].ID, engine.RestoreOptions{Target: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	breakThings(ctx, e, mem)
	if v, ok := e.LastVerify(); !ok || v.OK() {
		t.Fatal("broken demo didn't record verification failure")
	}
}
