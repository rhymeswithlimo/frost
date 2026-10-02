package chunker

import (
	"bytes"
	"crypto/sha256"
	"io"
	"math/rand"
	"testing"
)

func randBytes(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func split(t *testing.T, data []byte, tab *Table) [][]byte {
	t.Helper()
	c := New(bytes.NewReader(data), tab)
	var out [][]byte
	for {
		ch, err := c.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, append([]byte(nil), ch...))
	}
}

func TestReassemblesAndRespectsBounds(t *testing.T) {
	data := randBytes(40<<20, 1)
	chunks := split(t, data, NewTable(42))
	var joined []byte
	for i, ch := range chunks {
		if len(ch) > MaxSize {
			t.Fatalf("chunk %d is %d bytes, over max", i, len(ch))
		}
		if i < len(chunks)-1 && len(ch) < MinSize {
			t.Fatalf("chunk %d is %d bytes, under min", i, len(ch))
		}
		joined = append(joined, ch...)
	}
	if !bytes.Equal(joined, data) {
		t.Fatal("chunks don't reassemble to the input")
	}
	avg := len(data) / len(chunks)
	if avg < AvgSize/2 || avg > AvgSize*2 {
		t.Errorf("average chunk size %d is far from %d", avg, AvgSize)
	}
}

func TestInsertOnlyChangesNearbyChunks(t *testing.T) {
	tab := NewTable(7)
	data := randBytes(30<<20, 2)
	edited := append(append(append([]byte(nil), data[:5<<20]...), []byte("inserted!")...), data[5<<20:]...)

	seen := map[[32]byte]bool{}
	for _, ch := range split(t, data, tab) {
		seen[sha256.Sum256(ch)] = true
	}
	changed := 0
	for _, ch := range split(t, edited, tab) {
		if !seen[sha256.Sum256(ch)] {
			changed++
		}
	}
	if changed > 2 {
		t.Fatalf("a small insert changed %d chunks, want at most 2", changed)
	}
}

func TestSeedChangesBoundaries(t *testing.T) {
	data := randBytes(20<<20, 3)
	a := split(t, data, NewTable(1))
	b := split(t, data, NewTable(2))
	if len(a) == len(b) && len(a[0]) == len(b[0]) {
		t.Fatal("different seeds produced identical boundaries")
	}
}

func TestSmallAndEmpty(t *testing.T) {
	if got := split(t, nil, NewTable(1)); len(got) != 0 {
		t.Fatal("empty input should give no chunks")
	}
	if got := split(t, []byte("tiny"), NewTable(1)); len(got) != 1 || string(got[0]) != "tiny" {
		t.Fatal("small input should give one chunk")
	}
}

func TestSplitMatchesStreaming(t *testing.T) {
	tab := NewTable(11)
	data := append(randBytes(20<<20, 4), make([]byte, 20<<20)...) // zeros cut at MaxSize
	want := split(t, data, tab)
	got := Split(data, tab)
	if len(got) != len(want) {
		t.Fatalf("Split made %d chunks, streaming made %d", len(got), len(want))
	}
	for i := range got {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("chunk %d differs", i)
		}
	}
}

// A restore can resume from a partly written file because a prefix of the
// data splits into the same leading chunks as the whole of it. Only the last
// one, cut short, differs.
func TestPrefixKeepsLeadingChunks(t *testing.T) {
	tab := NewTable(5)
	data := append(randBytes(24<<20, 6), make([]byte, 12<<20)...)
	whole := split(t, data, tab)
	for _, cut := range []int{0, 1, MinSize, MinSize + 1, AvgSize, 9 << 20, 17<<20 + 12345, 26 << 20, len(data) - 1} {
		part := split(t, data[:cut], tab)
		for i, ch := range part {
			if i == len(part)-1 {
				break // may be cut short
			}
			if !bytes.Equal(ch, whole[i]) {
				t.Fatalf("prefix of %d bytes: chunk %d differs from the whole file's", cut, i)
			}
		}
	}
}

func TestResetReusesBuffer(t *testing.T) {
	tab := NewTable(9)
	a, b := randBytes(3<<20, 7), randBytes(5<<20, 8)
	c := New(bytes.NewReader(a), tab)
	for {
		if _, err := c.Next(); err == io.EOF {
			break
		}
	}
	c.Reset(bytes.NewReader(b))
	var joined []byte
	for {
		ch, err := c.Next()
		if err == io.EOF {
			break
		}
		joined = append(joined, ch...)
	}
	if !bytes.Equal(joined, b) {
		t.Fatal("chunker didn't start over after Reset")
	}
}
