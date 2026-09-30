package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

func benchmarkTree(n int) *tree {
	s := snapshot.Snapshot{Paths: []string{"/data"}}
	files := make([]snapshot.File, 0, n+1)
	files = append(files, snapshot.File{Path: "/data", Type: snapshot.TypeDir})
	for i := range n {
		files = append(files, snapshot.File{Path: fmt.Sprintf("/data/file-%06d", i), Type: snapshot.TypeFile, Size: 100})
	}
	return newTree(s, &snapshot.Tree{Files: files})
}

func BenchmarkSelectionCount(b *testing.B) {
	for _, n := range []int{1000, 100000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			m := model{tree: benchmarkTree(n), sel: map[string]bool{"/data": true}}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				m.countSel()
			}
		})
	}
}

func BenchmarkSnapshotView(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			snaps := make([]snapshot.Snapshot, n)
			for i := range snaps {
				snaps[i] = snapshot.Snapshot{ID: fmt.Sprintf("snapshot-%d", i), Time: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Hour)}
			}
			m := model{ctx: context.Background(), w: 120, h: 36}
			next, _ := m.Update(snapsMsg{snaps: snaps})
			m = next.(model)
			m.snapCur = n / 2
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				m.viewSnapshots()
			}
		})
	}
}

func BenchmarkInputText(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			f := field{value: strings.Repeat("a", n)}
			b.ReportAllocs()
			for b.Loop() {
				inputText(f, false, true, 56)
			}
		})
	}
}

func BenchmarkSelectAll(b *testing.B) {
	for _, n := range []int{1000, 100000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			m := model{tree: benchmarkTree(n), dir: "/data", sel: map[string]bool{}}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				next, _ := m.filesKey("a")
				m = next.(model)
			}
		})
	}
}

func BenchmarkTreeIndex(b *testing.B) {
	for _, n := range []int{1000, 100000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			s := snapshot.Snapshot{Paths: []string{"/data"}}
			t := &snapshot.Tree{Files: make([]snapshot.File, n+1)}
			t.Files[0] = snapshot.File{Path: "/data", Type: snapshot.TypeDir}
			for i := range n {
				t.Files[i+1] = snapshot.File{Path: fmt.Sprintf("/data/FILE-%06d", i), Type: snapshot.TypeFile, Size: 100}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				newTree(s, t)
			}
		})
	}
}
