package engine

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/rhymeswithlimo/frost/internal/crypto"
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
// decrypts and matches its ID. It also checks the newest snapshot's file
// list opens. The result is saved for `frost status`.
func (e *Engine) Verify(ctx context.Context, n int) (VerifyResult, error) {
	if n < 0 {
		return VerifyResult{}, fmt.Errorf("verification sample can't be negative")
	}
	ids, err := e.Repo.ChunkIDs(ctx)
	if err != nil {
		return VerifyResult{}, err
	}
	known := make(map[crypto.ID]int, len(ids))
	for _, id := range ids {
		known[id] = 0
	}
	if err := e.Manifest.AddChunks(known); err != nil {
		return VerifyResult{}, err
	}
	remote, err := e.Repo.Snapshots(ctx, e.Manifest.Snapshots())
	if err != nil {
		return VerifyResult{}, err
	}
	if err := e.Manifest.SetSnapshots(remote); err != nil {
		return VerifyResult{}, err
	}
	res := VerifyResult{Time: time.Now(), Total: e.Manifest.ChunkCount()}

	for _, id := range e.Manifest.SampleChunks(n) {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		res.Checked++
		if _, err := e.Repo.GetChunk(ctx, id); err != nil {
			res.Failures = append(res.Failures, err.Error())
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
			res.Failures = append(res.Failures, fmt.Sprintf("snapshot %s file list: %v", newest, err))
		} else {
			missing := make(map[string]bool)
			for _, f := range tree.Files {
				for _, c := range f.Chunks {
					id, err := crypto.ParseID(c)
					if _, ok := known[id]; err != nil || !ok {
						missing[c] = true
					}
				}
			}
			if len(missing) > 0 {
				res.Failures = append(res.Failures, fmt.Sprintf("snapshot %s references %d missing or invalid chunks", newest, len(missing)))
			}
		}
	}

	slices.Sort(res.Failures)
	return res, e.Manifest.PutMeta(metaVerify, res)
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
