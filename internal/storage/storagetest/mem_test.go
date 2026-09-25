package storagetest

import "testing"

func TestMemConformance(t *testing.T) { Conformance(t, NewMem()) }
