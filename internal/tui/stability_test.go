package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/storage"
	"github.com/rhymeswithlimo/frost/internal/storage/storagetest"
)

func TestSetupEmptyKey(t *testing.T) {
	m := newSetup(context.Background(), SetupDeps{}, config.Default(), false)
	m.step = stStorage
	m.Update(tea.KeyMsg{Type: tea.KeyRunes})
}

func TestCheckoutCancelWhileOpening(t *testing.T) {
	var checkoutCtx context.Context
	m := newSetup(context.Background(), SetupDeps{
		Checkout: func(ctx context.Context, _ config.Storage) (string, func() (string, error), error) {
			checkoutCtx = ctx
			return "page", func() (string, error) { return "", ctx.Err() }, nil
		},
	}, config.Default(), false)
	m.step = stCheckout
	next, cmd := m.startCheckout()
	m = next.(setupModel).stopCheckout()
	batch := cmd().(tea.BatchMsg)
	msg := batch[1]()
	if checkoutCtx.Err() != context.Canceled {
		t.Fatal("checkout was not cancelled while its browser was opening")
	}
	step(t, m, msg)
}

func TestCheckoutEmptyResult(t *testing.T) {
	m := newSetup(context.Background(), SetupDeps{}, config.Default(), false)
	m.step, m.co = stCheckout, checkoutRun{id: 1, waiting: true}
	next, _ := m.Update(checkoutMsg{id: 1})
	if next.(setupModel).co.waiting || next.(setupModel).co.failed == "" {
		t.Fatal("an empty checkout result should offer retry or pasting")
	}
}

func TestCheckoutCleansUpWithoutStartMessage(t *testing.T) {
	done := make(chan struct{})
	var waits atomic.Int32
	m := newSetup(context.Background(), SetupDeps{
		Checkout: func(ctx context.Context, _ config.Storage) (string, func() (string, error), error) {
			return "page", func() (string, error) {
				waits.Add(1)
				<-ctx.Done()
				close(done)
				return "", ctx.Err()
			}, nil
		},
	}, config.Default(), false)
	next, cmd := m.startCheckout()
	m = next.(setupModel)
	msg := cmd().(tea.BatchMsg)[1]().(checkoutStartedMsg)
	// The program can exit before receiving the start message.
	m.stopCheckout()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("checkout listener was not cleaned up after exit")
	}
	msg.wait()
	if waits.Load() != 1 {
		t.Fatal("checkout waited more than once")
	}
}

func TestCheckoutMissingWait(t *testing.T) {
	m := newSetup(context.Background(), SetupDeps{}, config.Default(), false)
	m.co = checkoutRun{id: 1, waiting: true}
	cancelled := false
	next, cmd := m.Update(checkoutStartedMsg{id: 1, cancel: func() { cancelled = true }})
	if cmd != nil || !cancelled || next.(setupModel).co.waiting || next.(setupModel).co.failed == "" {
		t.Fatal("checkout without a listener should fail safely")
	}
}

func TestRestorePickerAcrossAttempts(t *testing.T) {
	t.Cleanup(noDesktop)
	canPick = func() bool { return true }
	s := snapshot.Snapshot{ID: "x", Paths: []string{"/data"}}
	tr := newTree(s, &snapshot.Tree{Files: []snapshot.File{
		{Path: "/data", Type: snapshot.TypeDir},
		{Path: "/data/a", Type: snapshot.TypeFile},
	}})
	m := model{ctx: context.Background(), screen: scrFiles, snap: s, tree: tr, dir: "/data", sel: map[string]bool{}}
	next, _ := m.filesKey("r")
	next, _ = next.(model).startPick()
	m = next.(model)
	old := m.rs.pickSeq
	m.stopPick()
	m.screen = scrFiles
	next, _ = m.filesKey("r")
	next, _ = next.(model).startPick()
	m = next.(model)
	t.Cleanup(m.stopPick)
	next, _ = m.Update(folderPickedMsg{seq: old, path: t.TempDir()})
	if next.(model).rs.phase != phasePicking || next.(model).rs.picked != "" {
		t.Fatal("a previous restore's picker replaced the current destination")
	}
}

func TestDiffResizeClampsScroll(t *testing.T) {
	m := model{w: 80, h: 24, screen: scrDiff, changes: make([]snapshot.Change, 50)}
	m.diffTop = m.diffMaxTop()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	m = next.(model)
	if m.diffTop != m.diffMaxTop() {
		t.Fatalf("scroll position %d after resize, want %d", m.diffTop, m.diffMaxTop())
	}
}

func TestSelectionParentReplacesChildren(t *testing.T) {
	s := snapshot.Snapshot{Paths: []string{"/data"}}
	tr := newTree(s, &snapshot.Tree{Files: []snapshot.File{
		{Path: "/data", Type: snapshot.TypeDir},
		{Path: "/data/folder", Type: snapshot.TypeDir},
		{Path: "/data/folder/a", Type: snapshot.TypeFile, Size: 123},
	}})
	m := model{tree: tr, dir: "/data", sel: map[string]bool{"/data/folder/a": true}}
	next, _ := m.filesKey("a")
	m = next.(model)
	if len(m.sel) != 1 || !m.sel["/data/folder"] || m.selFiles != 1 || m.selBytes != 123 {
		t.Fatalf("parent selection: %v, %d files, %d bytes", m.sel, m.selFiles, m.selBytes)
	}
	next, _ = m.filesKey("a")
	m = next.(model)
	if len(m.sel) != 0 || m.selFiles != 0 || m.selBytes != 0 {
		t.Fatal("deselecting the parent left hidden child selections")
	}
}

func TestTruncateLeftTinyWidths(t *testing.T) {
	for w := 0; w <= 8; w++ {
		for _, s := range []string{"long-file.txt", "日本語.txt", "日本語", "👩‍💻"} {
			if got := truncateLeft(s, w); lipgloss.Width(got) > w {
				t.Fatalf("truncateLeft(%q, %d) = %q", s, w, got)
			}
		}
	}
}

func TestInputTextKeepsCursorAndGraphemes(t *testing.T) {
	for _, value := range []string{"abcdef", "日本語", "e\u0301e\u0301", "👩‍💻👩‍💻"} {
		for back := 0; back <= len([]rune(value)); back++ {
			for w := 2; w <= 12; w++ {
				f := field{value: value, back: back}
				v := inputText(f, false, true, w)
				if lipgloss.Width(v) > w {
					t.Fatalf("input width %d exceeds %d for %q", lipgloss.Width(v), w, value)
				}
				if back == 0 && !strings.HasSuffix(stripANSI(v), "█") {
					t.Fatalf("end cursor lost for %q: %q", value, stripANSI(v))
				}
			}
		}
	}
	if got := stripANSI(inputText(field{value: "👩‍💻"}, false, true, 3)); got != "👩‍💻█" {
		t.Fatalf("emoji cluster broken: %q", got)
	}
}

func TestRestoreWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan tea.Msg, 1)
	done := make(chan tea.Msg, 1)
	go func() { done <- waitFor(ctx, ch)() }()
	cancel()
	select {
	case msg := <-done:
		if msg != nil {
			t.Fatal("cancelled wait returned a message")
		}
	case <-time.After(time.Second):
		ch <- nil
		t.Fatal("restore wait remained blocked after cancellation")
	}
}

func TestRestoreProgressBounds(t *testing.T) {
	for _, done := range []int{-1, 0, 1, 10, int(^uint(0) >> 1)} {
		m := model{w: 80, h: 24, rs: restoreState{phase: phaseRunning, done: done, total: 1}}
		m.viewRestore()
	}
}

type headerCounter struct {
	storage.Backend
	reads atomic.Int32
}

func (b *headerCounter) Get(ctx context.Context, key string) ([]byte, error) {
	if strings.HasPrefix(key, "snapshots/") {
		b.reads.Add(1)
	}
	return b.Backend.Get(ctx, key)
}

func TestSnapshotRefreshReusesHeaders(t *testing.T) {
	e, _ := testEngine(t)
	r := *e.Repo
	b := &headerCounter{Backend: r.Backend}
	r.Backend = b
	m := newModel(context.Background(), &r, config.Default(), State{})
	next, _ := m.Update(m.loadSnaps()())
	m = next.(model)
	if len(m.snaps) == 0 || b.reads.Load() == 0 {
		t.Fatal("initial load did not fetch snapshot headers")
	}
	b.reads.Store(0)
	m.loadSnaps()()
	if b.reads.Load() != 0 {
		t.Fatal("refresh downloaded headers already loaded by the TUI")
	}
	m.marked = m.snaps[0].ID
	next, _ = m.Update(snapsMsg{snaps: m.snaps[1:]})
	if next.(model).marked != "" {
		t.Fatal("refresh kept a mark for a deleted snapshot")
	}
}

func TestIndexedSelectionTotals(t *testing.T) {
	for _, root := range []string{"/data", "/", "C:/data"} {
		p := strings.TrimSuffix(root, "/")
		s := snapshot.Snapshot{Paths: []string{root}}
		tr := newTree(s, &snapshot.Tree{Files: []snapshot.File{
			{Path: root, Type: snapshot.TypeDir},
			{Path: p + "/folder", Type: snapshot.TypeDir},
			{Path: p + "/folder/a", Type: snapshot.TypeFile, Size: 5},
			{Path: p + "/b", Type: snapshot.TypeFile, Size: 7},
			{Path: p + "/link", Type: snapshot.TypeSymlink, Size: 100},
			{Path: p + "/empty", Type: snapshot.TypeDir},
		}})
		for _, sel := range []map[string]bool{
			{root: true},
			{root: true, p + "/folder/a": true},
			{p + "/folder": true, p + "/b": true, p + "/link": true, p + "/empty": true},
		} {
			if files, bytes := tr.selectionTotals(sel); files != 2 || bytes != 12 {
				t.Fatalf("%s: totals %d files, %d bytes", root, files, bytes)
			}
		}
	}
}

func TestTreeOrderIsDeterministic(t *testing.T) {
	s := snapshot.Snapshot{Paths: []string{"/data"}}
	files := []snapshot.File{
		{Path: "/data", Type: snapshot.TypeDir},
		{Path: "/data/b", Type: snapshot.TypeFile},
		{Path: "/data/a", Type: snapshot.TypeFile},
		{Path: "/data/A", Type: snapshot.TypeFile},
		{Path: "/data/z", Type: snapshot.TypeDir},
	}
	want := []string{"/data/z", "/data/A", "/data/a", "/data/b"}
	for range 20 {
		tr := newTree(s, &snapshot.Tree{Files: files})
		if !slices.Equal(tr.children["/data"], want) {
			t.Fatalf("folder order: %v", tr.children["/data"])
		}
	}
}

func TestSingleFileRootCanBeRestored(t *testing.T) {
	for _, kind := range []snapshot.Type{snapshot.TypeFile, snapshot.TypeSymlink} {
		s := snapshot.Snapshot{ID: "x", Paths: []string{"/data/a"}}
		tr := newTree(s, &snapshot.Tree{Files: []snapshot.File{{Path: "/data/a", Type: kind, Size: 5}}})
		next, _ := (model{ctx: context.Background()}).Update(treeMsg{snap: s, tree: tr})
		m := next.(model)
		if m.dir != rootKey || !slices.Equal(m.tree.children[m.dir], s.Paths) {
			t.Fatal("single file root was hidden as an empty folder")
		}
		next, _ = m.filesKey("r")
		if next.(model).screen != scrRestore || !slices.Equal(next.(model).rs.paths, s.Paths) {
			t.Fatal("single file root could not be restored")
		}
	}
}

func TestInvalidUTF8CannotReachTerminal(t *testing.T) {
	for _, value := range []string{"name\x9b31m", "name\xff\xfe"} {
		if got := printable(value); strings.ContainsAny(got, "\u009b\uFFFD") || strings.Contains(got, "\x9b") || strings.Contains(got, "\xff") {
			t.Fatalf("invalid text was not made printable: %q", got)
		}
	}
}

func TestSnapshotEqualTimeOrder(t *testing.T) {
	e, _ := testEngine(t)
	m := newModel(context.Background(), e.Repo, config.Default(), StateFrom(e))
	for id, s := range m.st.Known {
		s.Time = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
		m.st.Known[id] = s
	}
	msg := m.loadSnaps()().(snapsMsg)
	if msg.err != nil || len(msg.snaps) < 2 {
		t.Fatal("could not load equal-time snapshots", msg.err)
	}
	for i := 1; i < len(msg.snaps); i++ {
		if msg.snaps[i-1].ID >= msg.snaps[i].ID {
			t.Fatal("equal-time snapshots have unstable ordering")
		}
	}
}

func TestSnapshotRowsKeepDateHeadings(t *testing.T) {
	m := model{}
	snaps := make([]snapshot.Snapshot, 100)
	for i := range snaps {
		snaps[i] = snapshot.Snapshot{ID: fmt.Sprint(i), Time: time.Date(2026, 9, 30, 0, 0, 0, 0, time.Local).Add(-time.Duration(i) * time.Hour)}
	}
	next, _ := m.Update(snapsMsg{snaps: snaps})
	m = next.(model)
	last := ""
	for i, s := range m.snaps {
		at := m.snapPositions[i]
		if m.snapLayout[at].idx != i || m.snapLayout[at].header != "" {
			t.Fatal("snapshot row points to a heading or another snapshot")
		}
		date := s.Time.Local().Format("Mon 02 Jan 2006")
		if date != last && (at == 0 || m.snapLayout[at-1].header != date) {
			t.Fatal("date heading lost while indexing snapshots")
		}
		last = date
	}
}

func TestZeroSizeFrames(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{{}, {Width: 80}, {Height: 24}, {Width: -1, Height: -1}} {
		for _, m := range []tea.Model{model{}, setupModel{}} {
			next, _ := m.Update(size)
			if next.View() != "" {
				t.Fatal("zero-size terminal should have no frame")
			}
		}
	}
}

func TestMetadataControlCharacters(t *testing.T) {
	e, _ := testEngine(t)
	m := newModel(context.Background(), e.Repo, config.Default(), State{})
	m.w, m.h, m.loading = 120, 36, ""
	evil := "name\r\x1b[2J\u009b31m"
	m.cfg.Paths, m.cfg.Exclude = []string{evil}, []string{evil}
	m.overlay = "settings"
	views := []string{m.View(), m.snapDetail(snapshot.Snapshot{ID: "x", Host: evil}, 60)}
	setup := newSetup(context.Background(), SetupDeps{}, config.Default(), false)
	setup.err, setup.note = evil, evil
	views = append(views, setup.render(page{question: "question"}, 60, 20))
	for _, v := range views {
		if strings.ContainsAny(v, "\r\u009b") || strings.Contains(v, "\x1b[2J") {
			t.Fatal("metadata controls reached the frame")
		}
	}
}

func TestArcadeSmallWindowFreezes(t *testing.T) {
	a := newTestArcade(t)
	a.resize(10, 4)
	before := a.ticks
	if cmd := a.tick(arcadeTickMsg{gen: a.gen}); cmd == nil || a.ticks != before {
		t.Fatal("game should wait for a usable window without advancing")
	}
	a.key("right")
	a.key(" ")
	if len(a.bullets) != 0 {
		t.Fatal("game accepted shots while its playfield was hidden")
	}
}

func TestArcadeTicksBelongToTheirGame(t *testing.T) {
	a, b := newTestArcade(t), newTestArcade(t)
	old := a.tickCmd()().(arcadeTickMsg)
	b.gen = old.gen
	if cmd := b.tick(old); cmd != nil || b.ticks != 0 {
		t.Fatal("a previous game's pending tick advanced the current game")
	}
}

type diffBlockingBackend struct {
	storage.Backend
	started chan string
	release <-chan struct{}
	fail    string
	err     error
}

func (b *diffBlockingBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if strings.HasPrefix(key, "trees/") {
		b.started <- key
		select {
		case <-b.release:
			if key == b.fail {
				return nil, b.err
			}
			if b.fail != "" {
				<-ctx.Done()
				return nil, ctx.Err()
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.Backend.Get(ctx, key)
}

func TestDiffLoadsTreesTogether(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			k, err := crypto.NewKey()
			if err != nil {
				t.Fatal(err)
			}
			mem := storagetest.NewMem()
			r, err := repo.Init(ctx, mem, k)
			if err != nil {
				t.Fatal(err)
			}
			from := snapshot.Snapshot{ID: snapshot.NewID(), Time: time.Now().Add(-time.Hour)}
			to := snapshot.Snapshot{ID: snapshot.NewID(), Time: from.Time.Add(time.Hour)}
			for i, s := range []snapshot.Snapshot{from, to} {
				tree := &snapshot.Tree{Files: []snapshot.File{{Path: "/data/file", Type: snapshot.TypeFile, Size: int64(i + 1)}}}
				if _, err := r.SaveSnapshot(ctx, s, tree, nil); err != nil {
					t.Fatal(err)
				}
			}
			release := make(chan struct{})
			bad := errors.New("tree unavailable")
			backend := &diffBlockingBackend{Backend: mem, started: make(chan string, 2), release: release, err: bad}
			if fail {
				backend.fail = "trees/" + from.ID
			}
			r.Backend = backend
			done := make(chan diffMsg, 1)
			go func() { done <- (model{ctx: ctx, repo: r}).loadDiff(to, from)().(diffMsg) }()
			for range 2 {
				select {
				case <-backend.started:
				case <-ctx.Done():
					t.Fatal("diff tree reads did not overlap")
				}
			}
			close(release)
			select {
			case msg := <-done:
				if fail {
					if !errors.Is(msg.err, bad) {
						t.Fatalf("diff error = %v, want backend error", msg.err)
					}
				} else if msg.err != nil || msg.from.ID != from.ID || msg.to.ID != to.ID || len(msg.changes) != 1 || msg.changes[0].Kind != snapshot.Modified {
					t.Fatalf("diff lost ordering or changes: %v", msg.err)
				}
			case <-ctx.Done():
				t.Fatal("diff did not finish")
			}
		})
	}
}

func TestTreeTotalsOnlyIndexFolders(t *testing.T) {
	tr := benchmarkTree(1000)
	if len(tr.totals) != 1 {
		t.Fatalf("cached %d totals for one folder", len(tr.totals))
	}
	if files, bytes := tr.selectionTotals(map[string]bool{"/data/file-000123": true}); files != 1 || bytes != 100 {
		t.Fatalf("single file totals = %d, %d", files, bytes)
	}
	for _, root := range []string{"/data/a", "C:/data/a"} {
		tr := newTree(snapshot.Snapshot{Paths: []string{root}}, &snapshot.Tree{Files: []snapshot.File{{Path: root, Type: snapshot.TypeFile, Size: 7}}})
		if len(tr.totals) != 0 {
			t.Fatal("cached a single file root's total")
		}
		if files, bytes := tr.selectionTotals(map[string]bool{root: true}); files != 1 || bytes != 7 {
			t.Fatal("single file root lost its total")
		}
	}
}
