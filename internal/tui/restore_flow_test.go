package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/desktop"
	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

func TestMain(m *testing.M) {
	noDesktop()
	os.Exit(m.Run())
}

// noDesktop stubs out everything that would open a real window.
func noDesktop() {
	canOpen = func() bool { return false }
	canPick = func() bool { return false }
	openFolder = func(string, bool) error { return errors.New("no file manager in tests") }
	pickFolder = func(context.Context, string, string) (string, error) {
		return "", errors.New("no folder picker in tests")
	}
}

func TestRestoreStateShow(t *testing.T) {
	s := snapshot.Snapshot{ID: "x", Paths: []string{"/home/me"}}
	tr := newTree(s, &snapshot.Tree{Files: []snapshot.File{
		{Path: "/home/me", Type: snapshot.TypeDir},
		{Path: "/home/me/docs", Type: snapshot.TypeDir},
		{Path: "/home/me/docs/a.txt", Type: snapshot.TypeFile},
		{Path: "/home/me/b.txt", Type: snapshot.TypeFile},
	}})
	for _, c := range []struct {
		paths      []string
		show, base string
		file       bool
	}{
		{[]string{"/home/me/docs/a.txt"}, "/home/me/docs/a.txt", "/home/me/docs", true},
		{[]string{"/home/me/docs"}, "/home/me/docs", "/home/me", false},
		{[]string{"/home/me/docs/a.txt", "/home/me/b.txt"}, "/home/me", "/home/me", false},
	} {
		rs := newRestoreState(s, c.paths, tr)
		if rs.show != c.show || rs.base != c.base || rs.showFile != c.file {
			t.Errorf("%q: got %q %q %v", c.paths, rs.show, rs.base, rs.showFile)
		}
		// /home/me doesn't exist here, so there's no option 1, and the
		// default is a new location, never overwriting.
		if rs.besideErr == nil || rs.dest != destNew {
			t.Errorf("%q: option 1 available, dest %d", c.paths, rs.dest)
		}
	}
}

func TestRestoreShowPath(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "frost-restore-x")
	for _, c := range []struct {
		rs       restoreState
		want     string
		wantFile bool
	}{
		{restoreState{folder: folder, base: "/home/me", show: "/home/me/a.txt", showFile: true}, filepath.Join(folder, "a.txt"), true},
		{restoreState{folder: folder, base: "/home", show: "/home/me"}, filepath.Join(folder, "me"), false},
		{restoreState{folder: folder, base: "/home/me", show: "/home/me"}, folder, false},
		{restoreState{folder: folder, base: "/", show: "/"}, folder, false},
		{restoreState{folder: folder, base: "", show: ""}, folder, false},
		{restoreState{folder: folder, base: "", show: "C:/a"}, filepath.Join(folder, "C", "a"), false},
		{restoreState{dest: destOverwrite, show: "/home/me/a.txt", showFile: true}, filepath.FromSlash("/home/me/a.txt"), true},
		{restoreState{dest: destOverwrite, show: "/"}, "", false},
		{restoreState{dest: destOverwrite, show: "C:/"}, "", false},
		{restoreState{dest: destOverwrite, show: ""}, "", false},
	} {
		got, file := c.rs.showPath()
		if got != c.want || file != c.wantFile {
			t.Errorf("%+v: got %q %v, want %q %v", c.rs, got, file, c.want, c.wantFile)
		}
	}
}

func TestRestoreOpensFolder(t *testing.T) {
	var opened []string
	openFolder = func(p string, file bool) error {
		opened = append(opened, p)
		return errors.New("no file manager")
	}
	desk := true
	canOpen = func() bool { return desk }
	t.Cleanup(noDesktop)
	m := model{rs: restoreState{phase: phaseRunning, folder: "out", base: "/home/me", show: "/home/me/a.txt", showFile: true}}

	_, cmd := m.updateRestore(restoreDoneMsg{err: errors.New("missing chunk")})
	if cmd != nil {
		t.Fatal("a failed restore opens the folder")
	}
	desk = false
	_, cmd = m.updateRestore(restoreDoneMsg{})
	if cmd != nil {
		t.Fatal("opens the folder without a desktop")
	}
	desk = true
	next, cmd := m.updateRestore(restoreDoneMsg{})
	if cmd == nil || next.(model).rs.phase != phaseDone {
		t.Fatal("a successful restore doesn't open the folder")
	}
	msg := cmd()
	if want := filepath.Join("out", "a.txt"); len(opened) != 1 || opened[0] != want {
		t.Fatalf("opened %q, want %q", opened, want)
	}
	next, _ = next.(model).updateRestore(msg)
	if !strings.Contains(next.(model).flash, "no file manager") {
		t.Fatalf("open failure not shown: %q", next.(model).flash)
	}
}

func TestRestoreChoices(t *testing.T) {
	m := model{screen: scrRestore, rs: restoreState{besideErr: errors.New("nope"), dest: destNew}}
	press := func(k string) {
		t.Helper()
		next, _ := m.restoreKey(k)
		m = next.(model)
	}
	press("1")
	if m.rs.dest != destNew || !strings.Contains(m.flash, "nope") {
		t.Fatalf("chose an unavailable option 1: %d %q", m.rs.dest, m.flash)
	}
	press("up") // wraps past option 1
	if m.rs.dest != destOverwrite {
		t.Fatalf("up from 2: %d", m.rs.dest)
	}
	press("down")
	if m.rs.dest != destNew {
		t.Fatalf("down from 3 past option 1: %d", m.rs.dest)
	}
	press("3")
	press("enter")
	if m.rs.phase != phaseConfirm || !strings.Contains(m.flash, "[y]") {
		t.Fatal("overwrite started without [y]")
	}
	m.rs.besideErr = nil
	press("down")
	if m.rs.dest != destBeside {
		t.Fatalf("down from 3: %d", m.rs.dest)
	}
}

func TestRestoreOverwriteUnavailable(t *testing.T) {
	m := model{w: 120, h: 36, screen: scrRestore, rs: restoreState{dest: destBeside, overErr: errors.New("/x is a link owned by another user")}}
	next, _ := m.restoreKey("3")
	m = next.(model)
	if m.rs.dest != destBeside || !strings.Contains(m.flash, "another user") {
		t.Fatalf("chose an unavailable overwrite: %d %q", m.rs.dest, m.flash)
	}
	next, _ = m.restoreKey("up") // wraps past option 3
	if next.(model).rs.dest != destNew {
		t.Fatalf("up from 1: %d", next.(model).rs.dest)
	}
	v := stripANSI(m.viewRestore())
	if !strings.Contains(v, "Option 3 isn't available: /x is a link owned by another user.") {
		t.Fatalf("no reason shown:\n%s", v)
	}

	// A failure before anything was written doesn't claim changes remain.
	m.rs = restoreState{phase: phaseDone, dest: destOverwrite, err: errors.New("nope")}
	v = stripANSI(m.viewRestore())
	if !strings.Contains(v, "No files were restored.") || strings.Contains(v, "Earlier changes remain") {
		t.Fatalf("failure before writing:\n%s", v)
	}
}

func TestRestorePicker(t *testing.T) {
	t.Cleanup(noDesktop)
	dir := t.TempDir()
	var answer func() (string, error)
	canPick = func() bool { return true }
	pickFolder = func(ctx context.Context, _, _ string) (string, error) { return answer() }
	m := model{ctx: context.Background(), screen: scrRestore, rs: restoreState{snap: snapshot.Snapshot{ID: "x"}, besideErr: errors.New("nope"), dest: destNew}}
	pick := func() {
		t.Helper()
		next, cmd := m.restoreKey("enter")
		m = next.(model)
		if m.rs.phase != phasePicking || cmd == nil {
			t.Fatal("enter on option 2 didn't open the picker")
		}
		next, _ = m.Update(cmd())
		m = next.(model)
	}

	answer = func() (string, error) { return "", desktop.ErrCanceled }
	pick()
	if m.rs.phase != phaseConfirm || m.rs.picked != "" || m.flash != "" {
		t.Fatalf("cancel: phase %d, picked %q, flash %q", m.rs.phase, m.rs.picked, m.flash)
	}

	answer = func() (string, error) { return dir, nil }
	pick()
	if m.rs.phase != phaseReady || m.rs.picked != dir || m.rs.chosen != filepath.Join(dir, "frost-restore-x") {
		t.Fatalf("picked: phase %d, %q, %q", m.rs.phase, m.rs.picked, m.rs.chosen)
	}
	for _, want := range []string{"frost-restore-x/", "[enter] restore", "[c] change location", "[esc] cancel"} {
		if v := stripANSI((model{w: 120, h: 36, rs: m.rs}).viewRestore()); !strings.Contains(v, want) {
			t.Fatalf("review screen missing %q:\n%s", want, v)
		}
	}

	// Changing the location and then cancelling keeps the one chosen.
	answer = func() (string, error) { return "", desktop.ErrCanceled }
	next, cmd := m.restoreKey("c")
	m = next.(model)
	next, _ = m.Update(cmd())
	if r := next.(model); r.rs.phase != phaseReady || r.rs.picked != dir {
		t.Fatalf("cancelled change: phase %d, %q", r.rs.phase, r.rs.picked)
	}
	m = next.(model)

	// Back on the options, [enter] goes to the review, not straight to a restore.
	m.rs.phase = phaseConfirm
	next, cmd = m.restoreKey("enter")
	if next.(model).rs.phase != phaseReady || cmd != nil {
		t.Fatal("enter with a folder chosen didn't show the review")
	}
	if next, _ := m.restoreKey("esc"); next.(model).screen != scrFiles {
		t.Fatal("esc on the options didn't cancel")
	}
	m = next.(model)
	if next, _ := m.restoreKey("esc"); next.(model).screen != scrFiles {
		t.Fatal("esc on the review didn't cancel")
	}

	// An answer from a picker that was given up on is ignored.
	next, _ = m.Update(folderPickedMsg{seq: m.rs.pickSeq - 1, path: t.TempDir()})
	if next.(model).rs.picked != dir {
		t.Fatal("a stale picker's answer was used")
	}

	answer = func() (string, error) { return "", errors.New("broken") }
	next, cmd = m.restoreKey("c")
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	if m.rs.phase != phaseTyping || !strings.Contains(m.flash, "broken") {
		t.Fatalf("picker error: phase %d, flash %q", m.rs.phase, m.flash)
	}
}

func TestRestoreTypedFolder(t *testing.T) {
	dir := t.TempDir()
	var m tea.Model = model{ctx: context.Background(), screen: scrRestore, rs: restoreState{snap: snapshot.Snapshot{ID: "x"}, besideErr: errors.New("nope"), dest: destNew}}
	m = step(t, m, key("enter")) // no picker here, so straight to typing
	if m.(model).rs.phase != phaseTyping || m.(model).flash == "" {
		t.Fatal("no picker didn't fall back to typing")
	}
	// [v] and [q] are typed, not shortcuts.
	m = step(t, m, key(filepath.Join(dir, "vq")))
	if m.(model).showKey || m.(model).rs.input.fields[0].value != filepath.Join(dir, "vq") {
		t.Fatalf("typing reached the browser: %q", m.(model).rs.input.fields[0].value)
	}
	m = step(t, m, key("enter"))
	if m.(model).rs.phase != phaseTyping || !strings.Contains(m.(model).rs.inputErr, "no folder") {
		t.Fatalf("a missing folder was accepted: %q", m.(model).rs.inputErr)
	}
	f := m.(model).rs.input
	f.fields[0].value = dir
	r := m.(model)
	r.rs.input = f
	m = step(t, r, key("enter"))
	if m.(model).rs.phase != phaseReady || m.(model).rs.picked != dir {
		t.Fatalf("typed folder not used: phase %d, %q", m.(model).rs.phase, m.(model).rs.picked)
	}
	m = step(t, m, key("c"))
	if m.(model).rs.phase != phaseTyping {
		t.Fatal("[c] didn't change the location")
	}
	m = step(t, m, key("esc"))
	if m.(model).rs.phase != phaseReady || m.(model).rs.picked != dir {
		t.Fatal("esc from typing lost the folder")
	}
}

// A real restore through the browser, into a new folder beside the
// originals and into one that was chosen.
func TestRestoreDestinations(t *testing.T) {
	e, src := testEngine(t)
	ctx := context.Background()
	snaps, err := e.Repo.Snapshots(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap := snaps[len(snaps)-1]
	for _, s := range snaps {
		if s.Time.After(snap.Time) {
			snap = s
		}
	}
	tr, err := e.Repo.LoadTree(ctx, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.ToSlash(filepath.Join(src, "Documents"))}
	run := func(m model) model {
		t.Helper()
		next, _ := m.restoreKey("enter")
		m = next.(model)
		if m.rs.phase == phaseReady {
			next, _ = m.restoreKey("enter")
			m = next.(model)
		}
		if m.rs.phase != phaseRunning {
			t.Fatalf("didn't start: flash %q, err %v", m.flash, m.rs.err)
		}
		for m.rs.phase == phaseRunning {
			next, _ := m.updateRestore(<-m.rs.ch)
			m = next.(model)
		}
		if m.rs.err != nil {
			t.Fatal(m.rs.err)
		}
		return m
	}
	fresh := func() model {
		return model{ctx: ctx, repo: e.Repo, cfg: config.Default(), screen: scrRestore, rs: newRestoreState(snap, paths, newTree(snap, tr))}
	}

	m := fresh()
	if m.rs.dest != destBeside || m.rs.besideErr != nil {
		t.Fatalf("option 1 unavailable: %v", m.rs.besideErr)
	}
	m = run(m)
	want := filepath.Join(src, "frost-restore-"+snap.ID, "Documents", "notes.md")
	if data, err := os.ReadFile(want); err != nil || string(data) != "hello, changed" {
		t.Fatalf("beside: %q, %v", data, err)
	}

	dir := t.TempDir()
	m = fresh()
	if err := m.rs.setPicked(dir); err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.rs.tops, " ") != "Documents/" {
		t.Fatalf("tops %q", m.rs.tops)
	}
	m = run(m)
	want = filepath.Join(dir, "frost-restore-"+snap.ID, "Documents", "new.txt")
	if data, err := os.ReadFile(want); err != nil || string(data) != "new file" {
		t.Fatalf("new location: %q, %v", data, err)
	}
}

func TestRestoreFolderTaken(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "frost-restore-x")
	m := model{rs: restoreState{snap: snapshot.Snapshot{ID: "x"}, dest: destBeside, beside: base}}
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	next, cmd := m.restoreKey("enter")
	r := next.(model)
	if cmd != nil || r.rs.phase != phaseConfirm || r.rs.beside != base+"-1" || r.flash == "" {
		t.Fatal("occupied destination wasn't changed and presented for confirmation")
	}
}

func TestRestoreFailureDetails(t *testing.T) {
	m := model{w: 120, h: 36, rs: restoreState{
		phase: phaseDone, folder: "restore-output", err: errors.New("missing chunk"),
		res: engine.RestoreResult{Files: 2, Bytes: 100},
	}}
	view := m.viewRestore()
	for _, want := range []string{"2 files completed", "restore-output", "Earlier changes remain", "missing chunk"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestLongSnapshotListLabel(t *testing.T) {
	s := snapshot.Snapshot{ID: "ability-original-0123456789abcdef"}
	s.Stats.Files = 52
	s.Stats.Bytes = 1024
	for _, width := range []int{12, 20, 40, 50, 68, 100} {
		label := snapshotListLabel(s, "  ", 2, 8, width)
		if lipgloss.Width(label) != width || !strings.HasSuffix(label, "  ") {
			t.Fatalf("width %d: missing right padding in %q", width, label)
		}
		if width >= 40 && (!strings.Contains(label, "52 files") || !strings.Contains(label, humanBytes(s.Stats.Bytes))) {
			t.Fatalf("width %d: %q", width, label)
		}
	}
}
