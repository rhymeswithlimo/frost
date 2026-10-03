package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

func TestOverflowFramesPaintEveryCell(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	e, _ := testEngine(t)
	cfg := config.Default()
	cfg.Paths = []string{"/backup/" + strings.Repeat("nested/", 20) + "documents"}
	cfg.Exclude = []string{strings.Repeat("longpattern", 30)}
	s := snapshot.Snapshot{ID: "maple-absurd-3f1c9a0b2e7", Time: time.Now(), Paths: []string{"/backup"}, Stats: snapshot.Stats{Skipped: 3, Kept: 2}}
	files := []snapshot.File{{Path: "/backup", Type: snapshot.TypeDir}}
	for i := range 100 {
		files = append(files, snapshot.File{Path: fmt.Sprintf("/backup/entry-%03d.txt", i), Type: snapshot.TypeFile, Size: 1})
		s.Paths = append(s.Paths, fmt.Sprintf("/backup/location-%03d", i))
	}
	for _, size := range [][2]int{{50, 20}, {80, 24}, {120, 20}} {
		m := newModel(context.Background(), e.Repo, cfg, State{Version: "v1.2.3-" + strings.Repeat("longversion", 20)})
		m.w, m.h, m.loading = size[0], size[1], ""
		m.snaps = []snapshot.Snapshot{s}
		m.indexSnapshots()
		m.snap, m.tree, m.dir = s, newTree(s, &snapshot.Tree{Files: files}), "/backup"
		m.selFiles, m.selBytes = 123456789, 999000000000
		m.diffFrom, m.diffTo = s, s
		m.diffAdd, m.diffDel, m.diffMod = 1234567, 2345678, 3456789
		for i := range 100 {
			m.changes = append(m.changes, snapshot.Change{Kind: snapshot.Added, Path: fmt.Sprintf("/backup/change-%03d.txt", i), New: &snapshot.File{Size: 1}})
		}
		for _, screen := range []screen{scrSnapshots, scrFiles, scrDiff} {
			m.screen = screen
			assertPaintedBackground(t, fmt.Sprintf("browser-%dx%d-%d", size[0], size[1], screen), m.View())
		}
		for _, overlay := range []string{"help", "settings"} {
			m.overlay = overlay
			for top := 0; top <= m.overlayMaxTop(); top++ {
				m.overlayTop = top
				assertPaintedBackground(t, fmt.Sprintf("%s-%dx%d-page%d", overlay, size[0], size[1], top), m.View())
			}
		}
		m.err = errors.New(strings.Repeat("Long diagnostic description. ", 30))
		for top := 0; top <= m.errorMaxTop(); top++ {
			m.errorTop = top
			assertPaintedBackground(t, fmt.Sprintf("error-%dx%d-page%d", size[0], size[1], top), m.View())
		}
	}
	for _, size := range [][2]int{{minW, minH}, {80, 24}} {
		for _, step := range []setupStep{stFolders, stSkip, stReview, stCheckout, stPhrase, stDone} {
			m := newSetup(context.Background(), SetupDeps{}, cfg, false)
			m.w, m.h, m.step, m.key = size[0], size[1], step, e.Repo.Key
			m.err = strings.Repeat("A long explanation keeps the answer visible. ", 30)
			m.note = strings.Repeat("More diagnostic detail. ", 20)
			m.co.failed = strings.Repeat("Checkout reported a detailed problem. ", 30)
			m.newRepo, m.elsewhere = true, "s3://backups/"+strings.Repeat("nested/", 40)
			m.savedRows = [][2]string{{"schedule", "not installed: " + strings.Repeat("scheduler details ", 30)}}
			_, maxTop := m.feedbackWindow()
			for top := 0; top <= maxTop; top++ {
				m.feedbackTop = top
				assertPaintedBackground(t, fmt.Sprintf("setup-%dx%d-%d-page%d", size[0], size[1], step, top), m.View())
			}
		}
	}
}

var backgroundSGR = regexp.MustCompile("\\x1b\\[[0-9;:]*m")

func assertPaintedBackground(t *testing.T, name, view string) {
	t.Helper()
	painted, row, col := false, 1, 1
	text := func(s string) {
		for _, r := range s {
			if r == '\n' {
				row, col = row+1, 1
				continue
			}
			if w := ansi.StringWidth(string(r)); w > 0 {
				if !painted {
					t.Fatalf("%s has a default-background cell at row %d, column %d", name, row, col)
				}
				col += w
			}
		}
	}
	from := 0
	for _, match := range backgroundSGR.FindAllStringIndex(view, -1) {
		text(view[from:match[0]])
		params := strings.Split(view[match[0]+2:match[1]-1], ";")
		for i := 0; i < len(params); i++ {
			code, _ := strconv.Atoi(strings.Split(params[i], ":")[0])
			switch {
			case code == 0 || code == 49:
				painted = false
			case code >= 40 && code <= 47 || code >= 100 && code <= 107:
				painted = true
			case code == 48 || code == 38:
				if code == 48 {
					painted = true
				}
				if i+1 < len(params) && !strings.Contains(params[i], ":") {
					switch params[i+1] {
					case "2":
						i += 4
					case "5":
						i += 2
					}
				}
			}
		}
		from = match[1]
	}
	text(view[from:])
}
