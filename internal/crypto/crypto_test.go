package crypto

import (
	"bytes"
	"errors"
	"testing"
)

func TestPhraseRoundTrip(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	p := k.Phrase()
	k2, err := KeyFromPhrase(p)
	if err != nil {
		t.Fatal(err)
	}
	if k2.Fingerprint() != k.Fingerprint() || k2.ChunkerSeed() != k.ChunkerSeed() {
		t.Fatal("key from phrase differs")
	}
}

func TestSealOpen(t *testing.T) {
	k, _ := NewKey()
	for _, pt := range [][]byte{{}, []byte("hello"), bytes.Repeat([]byte("a"), 100000)} {
		blob := k.Seal(pt, "chunk/x")
		got, err := k.Open(blob, "chunk/x")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatal("round trip mismatch")
		}
	}
}

func TestOpenRejects(t *testing.T) {
	k, _ := NewKey()
	other, _ := NewKey()
	blob := k.Seal([]byte("secret"), "a")

	if _, err := other.Open(blob, "a"); !errors.Is(err, ErrDecrypt) {
		t.Error("wrong key accepted")
	}
	if _, err := k.Open(blob, "b"); !errors.Is(err, ErrDecrypt) {
		t.Error("wrong associated data accepted")
	}
	tampered := append([]byte(nil), blob...)
	tampered[len(tampered)-1] ^= 1
	if _, err := k.Open(tampered, "a"); !errors.Is(err, ErrDecrypt) {
		t.Error("tampered blob accepted")
	}
	if _, err := k.Open(blob[:5], "a"); !errors.Is(err, ErrDecrypt) {
		t.Error("truncated blob accepted")
	}
}

func TestChunkIDKeyed(t *testing.T) {
	a, _ := NewKey()
	b, _ := NewKey()
	d := []byte("same content")
	if a.ChunkID(d) == b.ChunkID(d) {
		t.Fatal("chunk IDs should differ across keys")
	}
	if a.ChunkID(d) != a.ChunkID(d) {
		t.Fatal("chunk IDs should be deterministic")
	}
}
