package storage

import (
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
