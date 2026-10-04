package repo

import (
	"context"
	"sync"

	"github.com/rhymeswithlimo/frost/internal/crypto"
)

// fetchWorkers is how many chunks LoadTree downloads at a time.
const fetchWorkers = 8

// fetch is one chunk download, shared by every entry in the window that
// asks for the same ID.
type fetch struct {
	id   crypto.ID
	done chan struct{}
	data []byte
	err  error
	refs int // window entries still waiting for it
}

// Fetch downloads the chunks in ids, up to workers at a time, and calls fn
// with each one's data in order, from the calling goroutine. Downloads run
// at most 2*workers entries ahead of fn, which bounds memory. A chunk is
// downloaded once for as long as some entry in that window still needs it,
// so a long run of identical chunks (zeros in a disk image) costs one
// request. fn must not modify or keep data. Fetch stops at the first error.
func (r *Repo) Fetch(ctx context.Context, ids []crypto.ID, workers int, fn func(i int, data []byte) error) error {
	if len(ids) == 0 {
		return nil
	}
	workers = min(max(workers, 1), len(ids))
	window := min(2*workers, len(ids))
	ctx, cancel := context.WithCancel(ctx)
	jobs := make(chan *fetch, window)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		close(jobs)
		wg.Wait()
	}()
	for range workers {
		wg.Go(func() {
			for f := range jobs {
				if ctx.Err() != nil {
					f.err = ctx.Err()
				} else {
					f.data, f.err = r.GetChunk(ctx, f.id)
				}
				close(f.done)
			}
		})
	}

	live := make(map[crypto.ID]*fetch)
	queue := make([]*fetch, window)
	next := 0
	for i := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		for ; next < len(ids) && next < i+window; next++ {
			f := live[ids[next]]
			if f == nil {
				f = &fetch{id: ids[next], done: make(chan struct{})}
				live[f.id] = f
				jobs <- f // never blocks: at most window fetches are live
			}
			f.refs++
			queue[next%window] = f
		}
		f := queue[i%window]
		select {
		case <-f.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if f.err != nil {
			return f.err
		}
		if err := fn(i, f.data); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if f.refs--; f.refs == 0 {
			delete(live, f.id)
		}
		queue[i%window] = nil
	}
	return nil
}
