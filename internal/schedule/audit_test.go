package schedule

import (
	"strings"
	"testing"
	"time"
)

func TestSchedulerEscapesSpecialPaths(t *testing.T) {
	j := Job{Binary: "/path/100%/$HOME/frost", ConfigDir: "/config/%h/$USER", CacheDir: "/cache/%h/$USER", LogFile: "/log/100%.log", Every: time.Hour}
	svc, _ := SystemdUnits(j)
	if !strings.Contains(svc, "100%%/$$HOME") || !strings.Contains(svc, "%%h/$$USER") {
		t.Fatal(svc)
	}
	if line := CronLine(j); !strings.Contains(line, `100\%`) || !strings.Contains(line, `\%h`) {
		t.Fatal(line)
	}
	if !strings.Contains(svc, `--cache-dir "/cache/%%h/$$USER"`) {
		t.Fatal(svc)
	}
	if line := CronLine(j); !strings.Contains(line, `--cache-dir '/cache/\%h/$USER'`) {
		t.Fatal(line)
	}
}

func TestStripCronPreservesUnrelatedMarkerText(t *testing.T) {
	line := "* * * * * echo '# frost-backup'\n"
	if got := stripCron(line); got != line {
		t.Fatalf("removed unrelated job: %q", got)
	}
}

func TestWindowsTrailingSeparator(t *testing.T) {
	args := TaskArgs(Job{Binary: `C:\bin\frost.exe`, ConfigDir: `C:\backup config\`, Every: time.Hour})
	if !strings.Contains(strings.Join(args, " "), `--config-dir "C:\backup config\\"`) {
		t.Fatal(args)
	}
}
