package storage

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadBounded(t *testing.T) {
	if got, err := ReadBounded(strings.NewReader("abcd"), 4); err != nil || string(got) != "abcd" {
		t.Fatalf("boundary: %q, %v", got, err)
	}
	if _, err := ReadBounded(strings.NewReader("abcde"), 4); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestReadBoundedSize(t *testing.T) {
	for _, c := range []struct {
		name, data  string
		size, limit int64
		bad         bool
	}{
		{"exact", "abcd", 4, 4, false},
		{"empty", "", 0, 4, false},
		{"unknown", "abcd", -1, 4, false},
		{"longer", "abcde", 4, 8, true},
		{"shorter", "abc", 4, 8, true},
		{"oversized", "abcd", 4, 3, true},
		{"unknown oversized", "abcde", -1, 4, true},
		{"invalid limit", "", -1, -1, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReadBoundedSize(strings.NewReader(c.data), c.size, c.limit)
			if (err != nil) != c.bad || !c.bad && string(got) != c.data {
				t.Fatalf("read = %q, %v", got, err)
			}
		})
	}
}

func TestReadBoundedSizePreservesFinalError(t *testing.T) {
	failure := errors.New("body checksum failure")
	for _, final := range []error{failure, io.EOF} {
		r := &terminalReader{data: "data", err: final}
		got, err := ReadBoundedSize(r, 4, 8)
		if final == io.EOF {
			if err != nil || string(got) != "data" {
				t.Fatalf("final data with EOF: %q, %v", got, err)
			}
		} else if !errors.Is(err, failure) || got != nil {
			t.Fatalf("lost final read error: %q, %v", got, err)
		}
	}
}

func TestReadBoundedSizeTruncationErrors(t *testing.T) {
	for _, c := range []struct {
		data string
		want error
	}{{"", io.EOF}, {"dat", io.ErrUnexpectedEOF}} {
		if _, err := ReadBoundedSize(strings.NewReader(c.data), 4, 8); !errors.Is(err, c.want) {
			t.Fatalf("data %q: %v, want %v", c.data, err, c.want)
		}
	}
}

type terminalReader struct {
	data string
	err  error
}

func (r *terminalReader) Read(p []byte) (int, error) {
	if r.data == "" {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, r.err
}

func TestReadBoundedSizeNoProgress(t *testing.T) {
	for _, size := range []int64{0, 4} {
		r := &emptyReader{}
		if _, err := ReadBoundedSize(r, size, 8); !errors.Is(err, io.ErrNoProgress) {
			t.Fatalf("size %d: %v", size, err)
		}
		if r.reads != 100 {
			t.Fatalf("size %d: %d reads", size, r.reads)
		}
	}
}

type emptyReader struct{ reads int }

func (r *emptyReader) Read([]byte) (int, error) {
	r.reads++
	return 0, nil
}

func BenchmarkReadBounded(b *testing.B) {
	data := strings.Repeat("a", 8<<20)
	for _, size := range []int64{-1, int64(len(data))} {
		name := "unknown"
		if size >= 0 {
			name = "known"
		}
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := ReadBoundedSize(strings.NewReader(data), size, int64(len(data))); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
