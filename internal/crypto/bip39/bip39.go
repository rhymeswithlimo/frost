// Package bip39 encodes and decodes raw entropy as a BIP39 mnemonic using the
// official English wordlist.
//
// frost only needs the entropy <-> words mapping. The PBKDF2 seed derivation
// from the BIP39 spec is intentionally not implemented because the entropy
// itself is the master key.
package bip39

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

//go:embed english.txt
var englishRaw string

// Words is the official BIP39 English wordlist (2048 entries).
var Words = strings.Fields(englishRaw)

var wordIndex = func() map[string]int {
	m := make(map[string]int, len(Words))
	for i, w := range Words {
		m[w] = i
	}
	return m
}()

// ErrChecksum is returned when a mnemonic's checksum bits don't match.
var ErrChecksum = errors.New("bip39: checksum mismatch (check the words and their order)")

// Encode turns entropy (16, 20, 24, 28 or 32 bytes) into a mnemonic.
func Encode(entropy []byte) (string, error) {
	n := len(entropy)
	if n < 16 || n > 32 || n%4 != 0 {
		return "", fmt.Errorf("bip39: invalid entropy length %d", n)
	}
	csBits := n * 8 / 32
	sum := sha256.Sum256(entropy)

	// entropy || checksum bits, as one big integer
	b := new(big.Int).SetBytes(entropy)
	b.Lsh(b, uint(csBits))
	b.Or(b, big.NewInt(int64(sum[0]>>(8-csBits))))

	count := (n*8 + csBits) / 11
	words := make([]string, count)
	mask := big.NewInt(2047)
	for i := count - 1; i >= 0; i-- {
		idx := new(big.Int).And(b, mask).Int64()
		words[i] = Words[idx]
		b.Rsh(b, 11)
	}
	return strings.Join(words, " "), nil
}

// Decode turns a mnemonic back into its entropy, validating the checksum.
// Whitespace and case are normalised first.
func Decode(mnemonic string) ([]byte, error) {
	words := strings.Fields(strings.ToLower(mnemonic))
	switch len(words) {
	case 12, 15, 18, 21, 24:
	default:
		return nil, fmt.Errorf("bip39: expected 12 to 24 words, got %d", len(words))
	}

	b := new(big.Int)
	for _, w := range words {
		idx, ok := wordIndex[w]
		if !ok {
			return nil, fmt.Errorf("bip39: %q is not in the wordlist", w)
		}
		b.Lsh(b, 11)
		b.Or(b, big.NewInt(int64(idx)))
	}

	totalBits := len(words) * 11
	csBits := totalBits / 33
	entBytes := (totalBits - csBits) / 8

	cs := new(big.Int).And(b, big.NewInt(int64(1<<csBits-1))).Int64()
	b.Rsh(b, uint(csBits))

	entropy := make([]byte, entBytes)
	b.FillBytes(entropy)

	sum := sha256.Sum256(entropy)
	if int64(sum[0]>>(8-csBits)) != cs {
		return nil, ErrChecksum
	}
	return entropy, nil
}
