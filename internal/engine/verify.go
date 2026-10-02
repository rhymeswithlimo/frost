package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/storage"
)

// VerifyResult is the outcome of a verification pass.
type VerifyResult struct {
	Time     time.Time `json:"time"`
	Checked  int       `json:"checked"`
	Total    int       `json:"total"` // chunks known to the manifest
	Failures []string  `json:"failures,omitempty"`
}

// OK reports whether every sampled object checked out.
func (v VerifyResult) OK() bool { return len(v.Failures) == 0 }

// Verify downloads a random sample of n uploaded chunks and checks each
// decrypts and matches its ID. It also loads the newest snapshot's file list
// and checks every chunk it uses is known. With full, or when a sync is due,
// it first refreshes the chunk list from storage, which lists every chunk.
// A chunk found missing makes the next backup sync and upload it again. The
// result is saved for `frost status`.
func (e *Engine) Verify(ctx context.Context, n int, full bool) (VerifyResult, error) {
	if n < 0 {
		return VerifyResult{}, fmt.Errorf("verification sample can't be negative")
	}
	if full || e.syncDue() {
		if err := e.syncChunks(ctx); err != nil {
			return VerifyResult{}, err
		}
	}
	_, gone, err := e.RefreshSnapshots(ctx)
	if err != nil {
		return VerifyResult{}, err
	}
	res := VerifyResult{Time: time.Now(), Total: e.Manifest.ChunkCount()}
	if gone > 0 {
		res.Failures = append(res.Failures, GoneText(gone))
	}
	missing := false

	sample := e.Manifest.SampleChunks(n)
	errs := make([]error, len(sample))
	var wg sync.WaitGroup
	sem := make(chan struct{}, e.downloaders())
	for i, id := range sample {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			_, errs[i] = e.Repo.GetChunk(ctx, id)
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	for _, err := range errs {
		res.Checked++
		if err != nil {
			res.Failures = append(res.Failures, err.Error())
			missing = missing || errors.Is(err, storage.ErrNotFound)
		}
	}

	snaps := e.Manifest.Snapshots()
	var newest string
	var newestTime time.Time
	for id, s := range snaps {
		if s.Time.After(newestTime) {
			newest, newestTime = id, s.Time
		}
	}
	if newest != "" {
		res.Checked++
		tree, err := e.Repo.LoadTree(ctx, newest)
		if err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			res.Failures = append(res.Failures, fmt.Sprintf("snapshot %s file list: %v", newest, err))
			missing = missing || errors.Is(err, storage.ErrNotFound)
		} else {
			unknown := make(map[string]bool)
			for _, f := range tree.Files {
				for _, c := range f.Chunks {
					id, err := crypto.ParseID(c)
					if err != nil || !e.Manifest.HasChunk(id) {
						unknown[c] = true
					}
				}
			}
			if len(unknown) > 0 {
				res.Failures = append(res.Failures, fmt.Sprintf("snapshot %s references %d missing or invalid chunks", newest, len(unknown)))
				missing = true
			}
		}
	}
	if missing {
		if err := e.requestSync(); err != nil {
			return res, err
		}
	}

	slices.Sort(res.Failures)
	return res, e.Manifest.PutMeta(metaVerify, res)
}

// RefreshSnapshots fetches the snapshot headers from storage and caches
// them. missing counts snapshots this machine knew about that storage
// doesn't have any more. frost never deletes one, so they went missing from
// outside, like a folder moved without all its contents. They're reported
// once, then forgotten.
func (e *Engine) RefreshSnapshots(ctx context.Context) (snaps []snapshot.Snapshot, missing int, err error) {
	cached := e.Manifest.Snapshots()
	snaps, err = e.Repo.Snapshots(ctx, cached)
	if err != nil {
		return nil, 0, err
	}
	have := make(map[string]bool, len(snaps))
	for _, s := range snaps {
		have[s.ID] = true
	}
	for id := range cached {
		if !have[id] {
			missing++
		}
	}
	return snaps, missing, e.Manifest.SetSnapshots(snaps)
}

// GoneText says that n snapshots went missing (see RefreshSnapshots).
func GoneText(n int) string {
	if n == 1 {
		return "1 snapshot this machine knew about isn't in storage any more"
	}
	return fmt.Sprintf("%d snapshots this machine knew about aren't in storage any more", n)
}

// LastVerify returns the most recent verification result, if any.
func (e *Engine) LastVerify() (VerifyResult, bool) {
	var v VerifyResult
	ok := e.Manifest.GetMeta(metaVerify, &v)
	return v, ok
}

// LastBackup returns the most recent backup attempt, if any.
func (e *Engine) LastBackup() (LastRun, bool) {
	var r LastRun
	ok := e.Manifest.GetMeta(metaLastBackup, &r)
	return r, ok
}
