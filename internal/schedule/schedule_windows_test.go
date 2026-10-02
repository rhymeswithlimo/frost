package schedule

import (
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestTaskCommandUsesWindowsArgumentRules(t *testing.T) {
	j := Job{
		Binary:    `C:\program files\frost.exe`,
		ConfigDir: `C:\backup config\`,
		CacheDir:  `C:\cache & 100% !\`,
		LogFile:   `C:\cache & 100% !\frost.log`,
		Every:     time.Hour,
	}
	args, err := windows.DecomposeCommandLine(TaskArgs(j)[5])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{j.Binary, "backup", "--scheduled", "--config-dir", j.ConfigDir, "--cache-dir", j.CacheDir, "--log-file", j.LogFile}
	if !slices.Equal(args, want) {
		t.Fatalf("Windows parsed %q, want %q", args, want)
	}
}
