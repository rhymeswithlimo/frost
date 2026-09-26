// Command demo opens the snapshot browser on made-up data, so you can try the
// TUI without setting up storage or a key.
//
//	go run ./internal/tui/demo
//	go run ./internal/tui/demo -latency 400ms   # see the loading states
//	go run ./internal/tui/demo -empty           # a repository with no snapshots
//	go run ./internal/tui/demo -broken          # a failed backup and a failed health check
//	go run ./internal/tui/demo -setup           # the `frost init` screens (Permafrost key: demo)
//
// Everything lives in memory and a temp directory. Restores are written into
// that temp directory, and its path is printed when you quit.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/manifest"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/storage"
	"github.com/rhymeswithlimo/frost/internal/storage/storagetest"
	"github.com/rhymeswithlimo/frost/internal/tui"
)

func main() {
	latency := flag.Duration("latency", 0, "delay every storage call by this much")
	empty := flag.Bool("empty", false, "start with no snapshots")
	broken := flag.Bool("broken", false, "show a failed backup and a failed health check")
	setup := flag.Bool("setup", false, "open the setup screens instead of the browser")
	flag.Parse()

	if *setup {
		if err := runSetup(*latency); err != nil {
			fmt.Fprintln(os.Stderr, "demo:", err)
			os.Exit(1)
		}
		return
	}

	if err := run(*latency, *empty, *broken); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

func run(latency time.Duration, empty, broken bool) error {
	ctx := context.Background()
	work, err := os.MkdirTemp("", "frost-tui-demo-")
	if err != nil {
		return err
	}
	src := filepath.Join(work, "home")

	key, _ := crypto.NewKey()
	mem := storagetest.NewMem()
	r, err := repo.Init(ctx, mem, key)
	if err != nil {
		return err
	}
	m, err := manifest.Open(filepath.Join(work, "manifest.db"))
	if err != nil {
		return err
	}
	e := &engine.Engine{Repo: r, Manifest: m}

	if !empty {
		fmt.Println("Building demo snapshots...")
		if err := buildHistory(ctx, e, src); err != nil {
			return err
		}
		e.Verify(ctx, 10)
		if broken {
			breakThings(ctx, e, mem)
		}
	}

	cfg := config.Default()
	cfg.Paths = []string{filepath.Join(src, "Documents"), filepath.Join(src, "Pictures"), filepath.Join(src, "code")}
	cfg.Storage.Backend = "demo"

	st := tui.StateFrom(e)
	m.Close()

	// Restores default to ./frost-restore-<id>, so run from the temp dir.
	restores := filepath.Join(work, "restores")
	os.MkdirAll(restores, 0o755)
	os.Chdir(restores)

	r.Backend = &slow{Backend: mem, delay: latency}
	if err := tui.Run(ctx, r, cfg, st); err != nil {
		return err
	}
	fmt.Println("Demo files and any restores are in", work)
	return nil
}

// runSetup opens the setup screens with nothing behind them: the Permafrost
// access key "demo" connects, any other is refused, and S3 presets always
// connect. Nothing is saved.
func runSetup(latency time.Duration) error {
	wait := func() { time.Sleep(max(latency, 600*time.Millisecond)) }
	deps := tui.SetupDeps{
		Connect: func(_ context.Context, s config.Storage) (tui.RepoState, error) {
			wait()
			if s.Backend == "permafrost" && s.Permafrost.Token != "demo" {
				return 0, errors.New("that access key wasn't accepted, check you copied all of it (the demo key is: demo)")
			}
			return tui.RepoNew, nil
		},
		NewKey: crypto.NewKey,
		Unlock: func(_ context.Context, _ config.Storage, phrase string) (*crypto.Key, error) {
			wait()
			return crypto.KeyFromPhrase(phrase)
		},
		Finish: func(_ context.Context, cfg config.Config, _ *crypto.Key, _ bool) ([][2]string, error) {
			wait()
			return [][2]string{{"config", "not saved, this is the demo"}, {"schedule", cfg.Schedule.Every}}, nil
		},
		PickWords: func() (int, int) {
			i, j := rand.Intn(24), rand.Intn(23)
			if j >= i {
				j++
			}
			return min(i, j), max(i, j)
		},
		DirExists: func(p string) bool {
			_, err := os.Stat(config.Expand(p))
			return err == nil
		},
		Scheduler: "launchd",
	}
	res, err := tui.Setup(context.Background(), deps, config.Default(), false)
	if err != nil {
		return err
	}
	fmt.Println("demo setup finished, saved:", res.Saved)
	return nil
}

// buildHistory writes a small fake home directory and backs it up several
// times with changes in between, then spreads the snapshots over two weeks.
func buildHistory(ctx context.Context, e *engine.Engine, src string) error {
	rnd := rand.New(rand.NewSource(1))
	write := func(rel string, size int) {
		p := filepath.Join(src, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		b := make([]byte, size)
		rnd.Read(b)
		os.WriteFile(p, b, 0o644)
	}
	text := func(rel, s string) {
		p := filepath.Join(src, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}

	text("Documents/notes/todo.md", "- renew passport\n- call the bank\n")
	text("Documents/notes/ideas.md", "backup tool, but nice\n")
	write("Documents/taxes/2024/return.pdf", 420_000)
	write("Documents/taxes/2025/receipts.zip", 1_800_000)
	write("Documents/cv.pdf", 180_000)
	write("Pictures/2026/summer/beach-01.jpg", 3_200_000)
	write("Pictures/2026/summer/beach-02.jpg", 2_900_000)
	write("Pictures/2026/summer/sunset.jpg", 4_100_000)
	write("Pictures/avatar.png", 90_000)
	text("code/frost/README.md", "# frost\n")
	text("code/frost/main.go", "package main\n\nfunc main() {}\n")
	for i := range 40 {
		text(fmt.Sprintf("code/scratch/file-%02d.txt", i), fmt.Sprintf("scratch file %d\n", i))
	}

	paths := []string{filepath.Join(src, "Documents"), filepath.Join(src, "Pictures"), filepath.Join(src, "code")}
	changes := []func(){
		func() {},
		func() { text("Documents/notes/todo.md", "- renew passport\n- call the bank\n- buy milk\n") },
		func() { write("Pictures/2026/autumn/leaves.jpg", 2_500_000) },
		func() { os.RemoveAll(filepath.Join(src, "Documents/taxes/2024")) },
		func() { text("code/frost/main.go", "package main\n\nfunc main() { println(\"hi\") }\n") },
		func() { write("Documents/cv.pdf", 185_000) },
		func() { write("Pictures/2026/autumn/park.jpg", 3_000_000) },
		func() { text("Documents/notes/ideas.md", "backup tool, but nice\nsell it? no, keep it free\n") },
	}
	for _, change := range changes {
		change()
		if _, err := e.Backup(ctx, engine.BackupOptions{Paths: paths}); err != nil {
			return err
		}
	}

	// Backups just ran, so they're all timestamped "now". Rewrite their
	// times to look like a couple of weeks of history.
	snaps, err := e.Repo.Snapshots(ctx, nil)
	if err != nil {
		return err
	}
	now := time.Now()
	ages := []time.Duration{14 * 24 * time.Hour, 11*24*time.Hour + 3*time.Hour, 9 * 24 * time.Hour, 6*24*time.Hour + 5*time.Hour,
		4 * 24 * time.Hour, 2*24*time.Hour + 7*time.Hour, 26 * time.Hour, 3 * time.Hour}
	sortByTime(snaps)
	for i := range snaps {
		snaps[i].Time = now.Add(-ages[i%len(ages)]).UTC()
		tree, err := e.Repo.LoadTree(ctx, snaps[i].ID)
		if err != nil {
			return err
		}
		if err := e.Repo.SaveSnapshot(ctx, snaps[i], tree); err != nil {
			return err
		}
	}
	return e.Manifest.SetSnapshots(snaps)
}

func sortByTime(s []snapshot.Snapshot) {
	slices.SortFunc(s, func(a, b snapshot.Snapshot) int { return a.Time.Compare(b.Time) })
}

// breakThings corrupts a chunk and records a failed backup, to show the
// error states on the home screen.
func breakThings(ctx context.Context, e *engine.Engine, mem *storagetest.Mem) {
	keys, _ := mem.List(ctx, "chunks/")
	for _, k := range keys[:min(3, len(keys))] {
		b := mem.Raw(k)
		b[len(b)/2] ^= 0xff
	}
	e.Verify(ctx, len(keys))
	e.Backup(ctx, engine.BackupOptions{Paths: []string{"/does/not/exist"}})
}

// slow adds a fixed delay to every storage call.
type slow struct {
	storage.Backend
	delay time.Duration
}

func (s *slow) wait() { time.Sleep(s.delay) }

func (s *slow) Get(ctx context.Context, k string) ([]byte, error) {
	s.wait()
	return s.Backend.Get(ctx, k)
}

func (s *slow) List(ctx context.Context, p string) ([]string, error) {
	s.wait()
	return s.Backend.List(ctx, p)
}

func (s *slow) String() string { return "demo (in memory)" }
