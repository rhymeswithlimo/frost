package repo

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/storage/storagetest"
)

func TestConcurrentInitHasOneWinner(t *testing.T) {
	m := storagetest.NewMem()
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			k, _ := crypto.NewKey()
			if _, err := Init(context.Background(), m, k); err == nil {
				successes.Add(1)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful initializations: %d", successes.Load())
	}
}

func TestMetadataValidationAndImmutability(t *testing.T) {
	ctx := context.Background()
	k, _ := crypto.NewKey()
	m := storagetest.NewMem()
	r, err := Init(ctx, m, k)
	if err != nil {
		t.Fatal(err)
	}
	s := snapshot.Snapshot{ID: snapshot.NewID()}
	if _, err := r.SaveSnapshot(ctx, s, &snapshot.Tree{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SaveSnapshot(ctx, s, &snapshot.Tree{Files: []snapshot.File{{Path: "/changed"}}}, nil); err == nil {
		t.Fatal("overwrote snapshot")
	}
	tree, err := r.LoadTree(ctx, s.ID)
	if err != nil || len(tree.Files) != 0 {
		t.Fatal("original tree changed")
	}
	before, _ := r.ChunkIDs(ctx) // the file list's chunk
	id := k.ChunkID([]byte("x"))
	m.SetRaw("chunks/wrong/"+id.String(), []byte("x"))
	if ids, err := r.ChunkIDs(ctx); err != nil || len(ids) != len(before) {
		t.Fatal("noncanonical chunk counted")
	}
	r.Info.ID = "../../escape"
	raw, _ := json.Marshal(r.Info)
	m.SetRaw(infoKey, k.Seal(raw, infoKey))
	if _, err := Open(ctx, m, k); err == nil {
		t.Fatal("unsafe repository ID accepted")
	}
}

func TestInitRefusesOrphanedBackupObjects(t *testing.T) {
	m := storagetest.NewMem()
	m.SetRaw("trees/old", []byte("preserve"))
	k, _ := crypto.NewKey()
	if _, err := Init(context.Background(), m, k); err == nil {
		t.Fatal("initialized over existing backup objects")
	}
}
