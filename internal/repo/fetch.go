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
	if workers <= 0 {
		workers = 1
	}
	window := 2 * workers
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
	queue := make([]*fetch, 0, window)
	next := 0
	for i := range ids {
		for ; next < len(ids) && next < i+window; next++ {
			f := live[ids[next]]
			if f == nil {
				f = &fetch{id: ids[next], done: make(chan struct{})}
				live[f.id] = f
				jobs <- f // never blocks: at most window fetches are live
			}
			f.refs++
			queue = append(queue, f)
		}
		f := queue[0]
		queue = queue[1:]
		select {
		case <-f.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if f.err != nil {
			return f.err
		}
		if err := fn(i, f.data); err != nil {
			return err
		}
		if f.refs--; f.refs == 0 {
			delete(live, f.id)
		}
	}
	return nil
}
