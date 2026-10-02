package permafrost

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"

	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

// A snapshot's file list can be far bigger than Permafrost's 16 MiB object
// limit, because it's stored in chunks.
func TestFileListLargerThanObjectLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("uploads about 35 MB")
	}
	b, _ := newTest(t)
	ctx := context.Background()
	k, _ := crypto.NewKey()
	r, err := repo.Init(ctx, b, k)
	if err != nil {
		t.Fatal(err)
	}
	rnd := rand.New(rand.NewSource(1))
	tree := &snapshot.Tree{}
	for i := range 1000 {
		f := snapshot.File{Path: fmt.Sprintf("/vm/disk-%04d.img", i), Type: snapshot.TypeFile, Size: 500 << 20}
		for range 500 {
			var id crypto.ID
			rnd.Read(id[:])
			f.Chunks = append(f.Chunks, id.String())
		}
		tree.Files = append(tree.Files, f)
	}
	// Stored as one sealed object, as it used to be, it's refused.
	raw, _ := json.Marshal(tree)
	if err := b.PutNew(ctx, "trees/whole", k.Seal(raw, "trees/whole")); err == nil {
		t.Fatalf("a %d byte file list fit in one object, so this proves nothing", len(raw))
	}
	s := snapshot.Snapshot{ID: snapshot.NewID()}
	if _, err := r.SaveSnapshot(ctx, s, tree, nil); err != nil {
		t.Fatal(err)
	}
	got, err := r.LoadTree(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1000 || got.Files[999].Chunks[499] != tree.Files[999].Chunks[499] {
		t.Fatal("file list didn't come back intact")
	}
}
