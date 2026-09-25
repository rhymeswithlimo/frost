// Package chunker splits a stream into content-defined chunks using FastCDC
// (a gear rolling hash with normalized chunking).
//
// Boundaries depend on the content, not on offsets, so inserting a byte near
// the start of a file only changes the chunk or two around the edit. The rest
// of the file produces the same chunks as before and doesn't get re-uploaded.
package chunker

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"math/bits"
)

// Chunk size limits. Average is about 1 MiB.
const (
	MinSize = 256 << 10
	AvgSize = 1 << 20
	MaxSize = 8 << 20
)

// Masks use the top bits of the hash because those depend on the most input
// bytes. Before the average size a harder mask (more bits) makes a cut less
// likely, after it an easier mask makes one more likely. That keeps sizes
// close to the average.
var (
	avgBits = bits.Len(AvgSize) - 1
	maskS   = topBits(avgBits + 2)
	maskL   = topBits(avgBits - 2)
)

func topBits(n int) uint64 { return ^uint64(0) << (64 - n) }

// Table is a gear table: one random 64 bit value per byte value.
type Table [256]uint64

// NewTable derives a gear table from a seed. frost seeds it from the user's
// key so chunk boundaries (and therefore sizes) aren't predictable by anyone
// else.
func NewTable(seed uint64) *Table {
	var t Table
	var in [16]byte
	binary.LittleEndian.PutUint64(in[:8], seed)
	for i := range t {
		binary.LittleEndian.PutUint64(in[8:], uint64(i))
		sum := sha256.Sum256(in[:])
		t[i] = binary.LittleEndian.Uint64(sum[:8])
	}
	return &t
}

// Chunker reads from r and returns chunks one at a time.
type Chunker struct {
	r     io.Reader
	table *Table
	buf   []byte
	start int // first unconsumed byte in buf
	end   int // one past the last valid byte in buf
	eof   bool
}

// New returns a chunker over r.
func New(r io.Reader, t *Table) *Chunker {
	return &Chunker{r: r, table: t, buf: make([]byte, MaxSize*2)}
}

// Next returns the next chunk. The slice is only valid until the next call.
// It returns io.EOF once the stream is exhausted.
func (c *Chunker) Next() ([]byte, error) {
	if err := c.fill(); err != nil {
		return nil, err
	}
	n := c.end - c.start
	if n == 0 {
		return nil, io.EOF
	}
	cut := c.cutpoint(c.buf[c.start:c.end])
	chunk := c.buf[c.start : c.start+cut]
	c.start += cut
	return chunk, nil
}

// fill makes sure at least MaxSize bytes are buffered, unless at EOF.
func (c *Chunker) fill() error {
	if c.eof || c.end-c.start >= MaxSize {
		return nil
	}
	copy(c.buf, c.buf[c.start:c.end])
	c.end -= c.start
	c.start = 0
	for c.end < len(c.buf) && !c.eof {
		n, err := c.r.Read(c.buf[c.end:])
		c.end += n
		if err == io.EOF {
			c.eof = true
		} else if err != nil {
			return err
		}
	}
	return nil
}

// cutpoint returns the length of the next chunk in data.
func (c *Chunker) cutpoint(data []byte) int {
	n := len(data)
	if n <= MinSize {
		return n
	}
	if n > MaxSize {
		n = MaxSize
	}
	normal := min(n, AvgSize)

	var fp uint64
	i := MinSize
	for ; i < normal; i++ {
		fp = (fp << 1) + c.table[data[i]]
		if fp&maskS == 0 {
			return i + 1
		}
	}
	for ; i < n; i++ {
		fp = (fp << 1) + c.table[data[i]]
		if fp&maskL == 0 {
			return i + 1
		}
	}
	return n
}
