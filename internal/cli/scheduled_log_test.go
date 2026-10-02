package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rhymeswithlimo/frost/internal/config"
)

func TestScheduledBackupLogsResultsAndUpdates(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	configDir, cacheDir := os.Getenv("FROST_CONFIG_DIR"), os.Getenv("FROST_CACHE_DIR")
	u := stubUpdates(t, "v0.1.0")
	t.Setenv("FROST_CONFIG_DIR", configDir)
	t.Setenv("FROST_CACHE_DIR", cacheDir)
	u.checkErr = errors.New("network is down")
	path := filepath.Join(t.TempDir(), "log folder & 100% !", "frost.log")
	for range 2 {
		out := must(t, "", "backup", "--scheduled", "--log-file", path)
		if !strings.Contains(out, "SNAPSHOT") {
			t.Fatalf("backup output lost: %s", out)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"scheduled backup starting", "SNAPSHOT", "verified"} {
		if strings.Count(string(data), want) != 2 {
			t.Fatalf("log must contain two %q entries:\n%s", want, data)
		}
	}
	if !bytes.Contains(data, []byte("update check failed: network is down")) {
		t.Fatalf("update output missing:\n%s", data)
	}
	if bytes.Contains(data, []byte(f.key.Phrase())) || bytes.Contains(data, []byte("SECRET")) {
		t.Fatal("log contains credentials")
	}
}

func TestScheduledJobKeepsDirectories(t *testing.T) {
	setup(t)
	job, err := scheduledJob(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if job.ConfigDir != config.Dir() || job.CacheDir != config.CacheDir() || job.LogFile != logPath() {
		t.Fatalf("scheduled directories lost: %+v", job)
	}
	oldCache := config.CacheDir()
	t.Setenv("FROST_CACHE_DIR", t.TempDir())
	_, err = run(t, "", "backup", "--scheduled", "--config-dir", job.ConfigDir, "--cache-dir", job.CacheDir, "--log-file", job.LogFile)
	if err == nil {
		t.Fatal("backup without config succeeded")
	}
	if config.CacheDir() != oldCache {
		t.Fatalf("cache override lost: %s", config.CacheDir())
	}
	if data, err := os.ReadFile(job.LogFile); err != nil || !bytes.Contains(data, []byte("error:")) {
		t.Fatalf("wrong log location: %q, %v", data, err)
	}
}

func TestScheduledBackupLogsEarlyFailure(t *testing.T) {
	setup(t)
	path := filepath.Join(t.TempDir(), "frost.log")
	_, err := run(t, "", "backup", "--scheduled", "--log-file", path)
	if err == nil {
		t.Fatal("backup without config succeeded")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Contains(data, []byte("error: "+errorText(err))) {
		t.Fatalf("failure missing from log: %v, %s", readErr, data)
	}
}

func TestLogFileRequiresScheduledRun(t *testing.T) {
	setup(t)
	path := filepath.Join(t.TempDir(), "frost.log")
	_, err := run(t, "", "backup", "--log-file", path)
	if err == nil || !strings.Contains(err.Error(), "requires --scheduled") {
		t.Fatalf("log flag without scheduled run = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("log created for an invalid command: %v", err)
	}
}

func TestWindowsScheduledBackupLogsWithoutNewTask(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows default logging")
	}
	setup(t)
	_, err := run(t, "", "backup", "--scheduled")
	data, readErr := os.ReadFile(logPath())
	if err == nil || readErr != nil || !bytes.Contains(data, []byte("error:")) {
		t.Fatalf("default log = %s, %v; backup = %v", data, readErr, err)
	}
}

func TestScheduledLogFailureDoesNotStopBackup(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	out := must(t, "", "backup", "--scheduled", "--log-file", t.TempDir())
	if !strings.Contains(out, "couldn't open scheduled run log") || !strings.Contains(out, "SNAPSHOT") {
		t.Fatalf("missing warning or backup result: %s", out)
	}
}

func TestScheduledLogAppendsAndTrims(t *testing.T) {
	for _, size := range []int{8, (1 << 20) + 1} {
		path := filepath.Join(t.TempDir(), "frost.log")
		old := bytes.Repeat([]byte("x"), size)
		if err := os.WriteFile(path, old, 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := openScheduledLog(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (plainLogWriter{f}).Write([]byte("\x1b[31mnew\x1b[0m\n")); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		want := "new\n"
		if size <= 1<<20 {
			want = string(old) + want
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatalf("log = %q, %v", got, err)
		}
	}
}

func TestScheduledLogRefusesSymlink(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(outside, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "frost.log")
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if f, err := openScheduledLog(path); err == nil {
		f.Close()
		t.Fatal("symlink accepted")
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep me" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
}
