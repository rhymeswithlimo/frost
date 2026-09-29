package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/update"
)

// fakeUpdates stands in for the release server and the binary swap.
type fakeUpdates struct {
	latest    string // "" means nothing published
	checkErr  error
	installed []string
	installTo string
	failWith  error
	checks    int
}

func stubUpdates(t *testing.T, version string) *fakeUpdates {
	t.Helper()
	t.Setenv("FROST_CONFIG_DIR", t.TempDir())
	t.Setenv("FROST_CACHE_DIR", t.TempDir())
	f := &fakeUpdates{latest: "v0.2.0"}
	savedVersion := Version
	Version = version
	latestRelease = func(context.Context) (update.Release, error) {
		f.checks++
		if f.checkErr != nil {
			return update.Release{}, f.checkErr
		}
		if f.latest == "" {
			return update.Release{}, update.ErrNoRelease
		}
		return update.Release{Version: f.latest, Archive: "frost_x.tar.gz", Page: "https://example.com/" + f.latest}, nil
	}
	installRelease = func(_ context.Context, r update.Release, exe string) error {
		if f.failWith != nil {
			return f.failWith
		}
		f.installed = append(f.installed, r.Version)
		f.installTo = exe
		return nil
	}
	selfPath = func() (string, error) { return "/opt/frost/bin/frost", nil }
	canReplace = func(string) error { return nil }
	t.Cleanup(func() {
		Version = savedVersion
		latestRelease = update.Latest
		installRelease = update.Install
		selfPath = update.Executable
		canReplace = update.CanReplace
	})
	return f
}

func TestUpdateInstallsNewerRelease(t *testing.T) {
	f := stubUpdates(t, "v0.1.0")
	out := must(t, "", "update")
	if len(f.installed) != 1 || f.installed[0] != "v0.2.0" || f.installTo != "/opt/frost/bin/frost" {
		t.Fatalf("installed %v to %q\n%s", f.installed, f.installTo, out)
	}
	if !strings.Contains(out, "v0.2.0") || !strings.Contains(out, "installed") {
		t.Fatalf("output:\n%s", out)
	}
	st := update.LoadState(updateStatePath())
	if st.Installed != "v0.2.0" || st.From != "v0.1.0" || st.Latest != "v0.2.0" || st.Checked.IsZero() {
		t.Fatalf("state = %+v", st)
	}
}

func TestUpdateCheckDoesntInstall(t *testing.T) {
	f := stubUpdates(t, "v0.1.0")
	out := must(t, "", "update", "--check")
	if len(f.installed) != 0 || !strings.Contains(out, "frost update") {
		t.Fatalf("installed %v\n%s", f.installed, out)
	}
}

func TestUpdateNothingNewer(t *testing.T) {
	for _, v := range []string{"v0.2.0", "v0.3.0"} {
		f := stubUpdates(t, v)
		out := must(t, "", "update")
		if len(f.installed) != 0 || !strings.Contains(out, "latest release") {
			t.Fatalf("%s: installed %v\n%s", v, f.installed, out)
		}
	}
	f := stubUpdates(t, "v0.1.0")
	f.latest = ""
	if out := must(t, "", "update"); !strings.Contains(out, "no releases") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestUpdateRefusesDevBuild(t *testing.T) {
	f := stubUpdates(t, "dev")
	if _, err := run(t, "", "update"); !errors.Is(err, update.ErrDevBuild) {
		t.Fatalf("err = %v", err)
	}
	if f.checks != 0 {
		t.Fatal("dev build went online")
	}
}

func TestUpdateChecksWritableFirst(t *testing.T) {
	f := stubUpdates(t, "v0.1.0")
	canReplace = func(string) error { return errors.New("can't write to /usr/local/bin") }
	if _, err := run(t, "", "update"); err == nil || !strings.Contains(err.Error(), "can't write") {
		t.Fatalf("err = %v", err)
	}
	if f.checks != 0 {
		t.Fatal("went online before checking it could install")
	}
}

// Every installed frost checks a new binary by running it with --version
// and reading the last word. If that ever changes, old installs can't update.
func TestVersionOutputForUpdates(t *testing.T) {
	savedVersion := Version
	Version = "v9.9.9"
	t.Cleanup(func() { Version = savedVersion })
	f := strings.Fields(must(t, "", "--version"))
	if len(f) == 0 || f[len(f)-1] != "v9.9.9" {
		t.Fatalf("--version printed %q", f)
	}
}

func TestAutoUpdate(t *testing.T) {
	f := stubUpdates(t, "v0.1.0")
	var log bytes.Buffer
	autoUpdate(context.Background(), &log)
	if len(f.installed) != 1 || !strings.Contains(log.String(), "updated frost from v0.1.0 to v0.2.0") {
		t.Fatalf("installed %v\n%s", f.installed, log.String())
	}

	// At most once a day.
	autoUpdate(context.Background(), &log)
	if f.checks != 1 {
		t.Fatalf("checked %d times", f.checks)
	}

	// A clock that jumped back doesn't stop checks for good.
	st := update.LoadState(updateStatePath())
	st.Checked = time.Now().Add(48 * time.Hour)
	st.Save(updateStatePath())
	autoUpdate(context.Background(), &log)
	if f.checks != 2 {
		t.Fatalf("checked %d times after the clock went back", f.checks)
	}
}

func TestAutoUpdateOffOnlyTells(t *testing.T) {
	f := stubUpdates(t, "v0.1.0")
	cfg := config.Default()
	cfg.Update.Auto = false
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	autoUpdate(context.Background(), &log)
	if len(f.installed) != 0 || !strings.Contains(log.String(), "run `frost update`") {
		t.Fatalf("installed %v\n%s", f.installed, log.String())
	}
	if st := update.LoadState(updateStatePath()); st.Latest != "v0.2.0" {
		t.Fatalf("state = %+v", st)
	}
}

func TestAutoUpdateFailureIsRecorded(t *testing.T) {
	f := stubUpdates(t, "v0.1.0")
	f.failWith = errors.New("can't write to /usr/local/bin")
	var log bytes.Buffer
	autoUpdate(context.Background(), &log)
	st := update.LoadState(updateStatePath())
	if st.Error == "" || st.Installed != "" || !strings.Contains(log.String(), "failed") {
		t.Fatalf("state = %+v\n%s", st, log.String())
	}
	text, warn := updateSummary(config.Default(), st, time.Now())
	if !warn || !strings.Contains(text, "frost update") {
		t.Fatalf("summary = %q, %v", text, warn)
	}
}

func TestAutoUpdateSkipsDevBuilds(t *testing.T) {
	f := stubUpdates(t, "dev")
	autoUpdate(context.Background(), &bytes.Buffer{})
	if f.checks != 0 {
		t.Fatal("dev build went online")
	}
}

// A failed update never fails the scheduled backup it runs after.
func TestScheduledBackupSurvivesFailedUpdate(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	configDir, cacheDir := os.Getenv("FROST_CONFIG_DIR"), os.Getenv("FROST_CACHE_DIR")
	u := stubUpdates(t, "v0.1.0")
	// stubUpdates moved these; point them back at the set-up ones.
	t.Setenv("FROST_CONFIG_DIR", configDir)
	t.Setenv("FROST_CACHE_DIR", cacheDir)
	u.checkErr = errors.New("network is down")
	out, err := run(t, "", "backup", "--scheduled")
	if err != nil {
		t.Fatalf("backup failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "update check failed: network is down") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestUpdateSummary(t *testing.T) {
	savedVersion := Version
	Version = "v0.1.0"
	t.Cleanup(func() { Version = savedVersion })
	now := time.Now()
	on, off, noSched := config.Default(), config.Default(), config.Default()
	off.Update.Auto = false
	noSched.Schedule.Enabled = false
	for _, c := range []struct {
		cfg  config.Config
		st   update.State
		want string
		warn bool
	}{
		{on, update.State{}, "automatic", false},
		{off, update.State{}, "off", false},
		{noSched, update.State{}, "automatic, but only after scheduled backups, which are off", false},
		{on, update.State{Latest: "v0.2.0"}, "automatic, v0.2.0 installs after the next backup", false},
		{off, update.State{Latest: "v0.2.0"}, "off, v0.2.0 is out: run `frost update`", true},
		{on, update.State{Latest: "v0.1.0"}, "automatic", false},
		{on, update.State{Latest: "v0.2.0", Error: "boom"}, "v0.2.0 is out, but the last update failed: boom. Run `frost update`", true},
		{on, update.State{Installed: "v0.1.0", From: "v0.0.9", InstalledAt: now.Add(-time.Hour)}, "automatic, updated from v0.0.9 1h ago", false},
		{on, update.State{Installed: "v0.1.0", From: "v0.0.9", InstalledAt: now.Add(-30 * 24 * time.Hour)}, "automatic", false},
	} {
		got, warn := updateSummary(c.cfg, c.st, now)
		if got != c.want || warn != c.warn {
			t.Errorf("updateSummary(%+v) = %q, %v, want %q, %v", c.st, got, warn, c.want, c.warn)
		}
	}
	Version = "dev"
	if got, _ := updateSummary(on, update.State{Latest: "v0.2.0"}, now); got != "not for development builds" {
		t.Errorf("dev build: %q", got)
	}
}

// The config written by init includes the update setting, and old configs
// without it get the default.
func TestUpdateAutoSetting(t *testing.T) {
	t.Setenv("FROST_CONFIG_DIR", t.TempDir())
	os.WriteFile(config.Path(), []byte("paths = [\"/x\"]\n"), 0o600)
	c, err := config.LoadFile()
	if err != nil || !c.Update.Auto {
		t.Fatalf("old config: auto = %v, %v", c.Update.Auto, err)
	}
	must(t, "", "config", "set", "update.auto", "false")
	if out := must(t, "", "config", "get", "update.auto"); strings.TrimSpace(out) != "false" {
		t.Fatalf("get = %q", out)
	}
}
