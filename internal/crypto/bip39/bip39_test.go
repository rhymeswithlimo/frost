package bip39

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// Official English vectors from trezor/python-mnemonic (vectors.json,
// trimmed to the English set).
func TestVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string][][]string
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v["english"]) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range v["english"] {
		ent, _ := hex.DecodeString(c[0])
		got, err := Encode(ent)
		if err != nil {
			t.Fatal(err)
		}
		if got != c[1] {
			t.Errorf("Encode(%s) = %q, want %q", c[0], got, c[1])
		}
		back, err := Decode(c[1])
		if err != nil {
			t.Fatalf("Decode(%q): %v", c[1], err)
		}
		if hex.EncodeToString(back) != c[0] {
			t.Errorf("Decode round trip mismatch for %s", c[0])
		}
	}
}

func TestWordlist(t *testing.T) {
	if len(Words) != 2048 {
		t.Fatalf("wordlist has %d words", len(Words))
	}
}

func TestDecodeRejects(t *testing.T) {
	bad := []string{
		"abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon", // bad checksum
		"abandon abandon",
		"abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon notaword",
	}
	for _, m := range bad {
		if _, err := Decode(m); err == nil {
			t.Errorf("Decode(%q) succeeded", m)
		}
	}
}
