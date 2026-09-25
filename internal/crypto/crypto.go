// Package crypto holds frost's key material and everything that touches it:
// sealing and opening blobs, keyed chunk IDs, and the chunker seed.
//
// One 256-bit master key is generated on first run. Every other key is derived
// from it with HKDF-SHA256 under a distinct label, so no single subkey can be
// used for more than one job.
package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"github.com/rhymeswithlimo/frost/internal/crypto/bip39"
)

// KeySize is the size of the master key in bytes (a 24 word phrase).
const KeySize = 32

// blobVersion is the first byte of every sealed blob.
const blobVersion = 1

const (
	flagRaw  byte = 0
	flagZstd byte = 1
)

// ErrDecrypt means a blob failed authentication: wrong key or tampered data.
var ErrDecrypt = errors.New("decryption failed: wrong key or corrupted data")

// ID is a chunk identifier: HMAC-SHA256 of the plaintext under the ID key.
type ID [32]byte

// String returns the lowercase hex form.
func (id ID) String() string { return hex.EncodeToString(id[:]) }

// ParseID parses a hex chunk ID.
func ParseID(s string) (ID, error) {
	var id ID
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(id) {
		return id, fmt.Errorf("invalid chunk id %q", s)
	}
	copy(id[:], b)
	return id, nil
}

// Key is an unlocked master key and its derived subkeys.
type Key struct {
	master [KeySize]byte
	enc    [32]byte // AEAD key for all blobs
	mac    [32]byte // HMAC key for chunk IDs
	gear   uint64   // seed for the chunker's gear table
}

// NewKey generates a fresh random master key.
func NewKey() (*Key, error) {
	var m [KeySize]byte
	if _, err := rand.Read(m[:]); err != nil {
		return nil, err
	}
	return fromMaster(m), nil
}

// KeyFromPhrase unlocks a key from its BIP39 recovery phrase.
func KeyFromPhrase(phrase string) (*Key, error) {
	ent, err := bip39.Decode(phrase)
	if err != nil {
		return nil, err
	}
	if len(ent) != KeySize {
		return nil, fmt.Errorf("recovery phrase must be 24 words")
	}
	var m [KeySize]byte
	copy(m[:], ent)
	return fromMaster(m), nil
}

func fromMaster(m [KeySize]byte) *Key {
	k := &Key{master: m}
	derive(m[:], "frost v1 encryption", k.enc[:])
	derive(m[:], "frost v1 chunk id", k.mac[:])
	var g [8]byte
	derive(m[:], "frost v1 chunker", g[:])
	k.gear = binary.LittleEndian.Uint64(g[:])
	return k
}

func derive(secret []byte, label string, out []byte) {
	r := hkdf.New(sha256.New, secret, nil, []byte(label))
	if _, err := io.ReadFull(r, out); err != nil {
		panic(err) // only fails if out is absurdly large
	}
}

// Phrase returns the 24 word BIP39 recovery phrase for this key.
func (k *Key) Phrase() string {
	p, err := bip39.Encode(k.master[:])
	if err != nil {
		panic(err) // master is always 32 bytes
	}
	return p
}

// Fingerprint is a short, non-secret label for the key, safe to display.
func (k *Key) Fingerprint() string {
	var out [6]byte
	derive(k.master[:], "frost v1 fingerprint", out[:])
	return hex.EncodeToString(out[:])
}

// ChunkerSeed seeds the content-defined chunker so chunk boundaries depend on
// the key. With a public gear table, chunk sizes alone could fingerprint files.
func (k *Key) ChunkerSeed() uint64 { return k.gear }

// ChunkID returns the keyed ID of a plaintext chunk. Because it's an HMAC and
// not a bare hash, the storage provider can't check whether you have a known file.
func (k *Key) ChunkID(data []byte) ID {
	h := hmac.New(sha256.New, k.mac[:])
	h.Write(data)
	var id ID
	copy(id[:], h.Sum(nil))
	return id
}

var (
	zenc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	zdec, _ = zstd.NewReader(nil)
)

// Seal compresses (when it helps) and encrypts plaintext with
// XChaCha20-Poly1305. The associated data binds the blob to its name, so a
// provider can't swap one valid blob for another.
//
// Layout: version(1) | nonce(24) | AEAD(flag(1) | payload)
func (k *Key) Seal(plaintext []byte, ad string) []byte {
	body := make([]byte, 0, len(plaintext)+1)
	comp := zenc.EncodeAll(plaintext, nil)
	if len(comp) < len(plaintext) {
		body = append(append(body, flagZstd), comp...)
	} else {
		body = append(append(body, flagRaw), plaintext...)
	}

	aead, _ := chacha20poly1305.NewX(k.enc[:])
	out := make([]byte, 1+aead.NonceSize(), 1+aead.NonceSize()+len(body)+aead.Overhead())
	out[0] = blobVersion
	nonce := out[1 : 1+aead.NonceSize()]
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return aead.Seal(out, nonce, body, []byte(ad))
}

// Open reverses Seal. Any tampering, a wrong key or a mismatched name returns
// ErrDecrypt.
func (k *Key) Open(blob []byte, ad string) ([]byte, error) {
	aead, _ := chacha20poly1305.NewX(k.enc[:])
	ns := aead.NonceSize()
	if len(blob) < 1+ns+aead.Overhead()+1 || blob[0] != blobVersion {
		return nil, ErrDecrypt
	}
	body, err := aead.Open(nil, blob[1:1+ns], blob[1+ns:], []byte(ad))
	if err != nil || len(body) == 0 {
		return nil, ErrDecrypt
	}
	switch body[0] {
	case flagRaw:
		return body[1:], nil
	case flagZstd:
		out, err := zdec.DecodeAll(body[1:], nil)
		if err != nil {
			return nil, ErrDecrypt
		}
		return out, nil
	}
	return nil, ErrDecrypt
}
