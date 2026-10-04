package crypto

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"math/rand"
	"sync"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
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

func TestSealLimit(t *testing.T) {
	k, _ := NewKey()
	defer func() {
		if recover() == nil {
			t.Fatal("Seal accepted a plaintext that Open would refuse")
		}
	}()
	k.Seal(make([]byte, MaxPlaintextSize+1), "chunk/x")
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
	reloaded, err := KeyFromPhrase(a.Phrase())
	if err != nil {
		t.Fatal(err)
	}
	if a.ChunkID(d) != reloaded.ChunkID(d) {
		t.Fatal("chunk IDs should be deterministic")
	}
}

func TestSealFormatCompatibility(t *testing.T) {
	k, _ := NewKey()
	aead, _ := chacha20poly1305.NewX(k.enc[:])
	random := make([]byte, 1<<20)
	rand.New(rand.NewSource(1)).Read(random)
	for _, pt := range [][]byte{nil, []byte("hello"), bytes.Repeat([]byte("a"), 1<<20), random} {
		payload, flag := pt, flagRaw
		if comp := zenc.EncodeAll(pt, nil); len(comp) < len(pt) {
			payload, flag = comp, flagZstd
		}
		body := append([]byte{flag}, payload...)
		header := 1 + aead.NonceSize()
		legacy := make([]byte, header)
		legacy[0] = blobVersion
		legacy = aead.Seal(legacy, legacy[1:header], body, []byte("test/object"))
		got, err := k.Open(legacy, "test/object")
		if err != nil || !bytes.Equal(got, pt) {
			t.Fatalf("couldn't open existing blob format: %v", err)
		}
		blob := k.Seal(pt, "test/object")
		got, err = aead.Open(nil, blob[1:header], blob[header:], []byte("test/object"))
		if blob[0] != blobVersion || err != nil || !bytes.Equal(got, body) {
			t.Fatalf("sealed body changed its format: %v", err)
		}
	}
}

func TestConcurrentCrypto(t *testing.T) {
	k, _ := NewKey()
	var wg sync.WaitGroup
	for worker := range 24 {
		wg.Go(func() {
			data := bytes.Repeat([]byte{byte(worker)}, 1024+worker)
			mac := hmac.New(sha256.New, k.mac[:])
			mac.Write(data)
			var want ID
			copy(want[:], mac.Sum(nil))
			for range 20 {
				if got := k.ChunkID(data); got != want {
					t.Error("concurrent chunk ID changed")
					return
				}
				blob := k.Seal(data, "test/concurrent")
				got, err := k.Open(blob, "test/concurrent")
				if err != nil || !bytes.Equal(got, data) {
					t.Errorf("concurrent blob round trip: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestParseID(t *testing.T) {
	var want ID
	for i := range want {
		want[i] = byte(i)
	}
	if got, err := ParseID(want.String()); err != nil || got != want {
		t.Fatalf("ID round trip: %v", err)
	}
	for _, s := range []string{"", "abcd", string(bytes.Repeat([]byte("z"), 64)), want.String()[:63] + "z", want.String() + "0"} {
		if got, err := ParseID(s); err == nil || got != (ID{}) {
			t.Errorf("accepted malformed ID %q", s)
		}
	}
}

func TestZeroKeyChunkID(t *testing.T) {
	var k Key
	for _, data := range [][]byte{[]byte("first"), nil, []byte("second"), []byte("first")} {
		mac := hmac.New(sha256.New, make([]byte, 32))
		mac.Write(data)
		var want ID
		copy(want[:], mac.Sum(nil))
		if got := k.ChunkID(data); got != want {
			t.Fatal("zero-value key's chunk ID changed")
		}
	}
}

func BenchmarkChunkID(b *testing.B) {
	k, _ := NewKey()
	for _, c := range []struct {
		name string
		size int
	}{{"small", 32}, {"1MiB", 1 << 20}} {
		b.Run(c.name, func(b *testing.B) {
			data := make([]byte, c.size)
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				k.ChunkID(data)
			}
		})
	}
}

func BenchmarkSeal(b *testing.B) {
	k, _ := NewKey()
	for _, c := range []struct {
		name string
		size int
	}{{"small", 128}, {"1MiB", 1 << 20}, {"8MiB", 8 << 20}} {
		for _, compressed := range []bool{false, true} {
			name := c.name + "/random"
			if compressed {
				name = c.name + "/compressed"
			}
			b.Run(name, func(b *testing.B) {
				data := make([]byte, c.size)
				if compressed {
					copy(data, bytes.Repeat([]byte("abcdef"), c.size/6+1))
				} else {
					rand.New(rand.NewSource(1)).Read(data)
				}
				k.Seal(data, "chunk/test")
				b.SetBytes(int64(len(data)))
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					k.Seal(data, "chunk/test")
				}
			})
		}
	}
}
