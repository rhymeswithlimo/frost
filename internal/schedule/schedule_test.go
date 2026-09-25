package schedule

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

var job = Job{Binary: "/usr/local/bin/frost", Every: 6 * time.Hour, LogFile: "/home/me/.cache/frost/frost.log"}

func TestLaunchdPlistIsValidXML(t *testing.T) {
	j := job
	j.ConfigDir = "/tmp/a & b"
	p := LaunchdPlist(j)
	if err := xml.Unmarshal([]byte(p), new(any)); err != nil {
		t.Fatalf("invalid plist: %v\n%s", err, p)
	}
	for _, want := range []string{"<integer>21600</integer>", "<string>--scheduled</string>", "/tmp/a &amp; b"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}

func TestSystemd(t *testing.T) {
	svc, tmr := SystemdUnits(job)
	if !strings.Contains(svc, `ExecStart="/usr/local/bin/frost" backup --scheduled`) {
		t.Errorf("service:\n%s", svc)
	}
	if !strings.Contains(tmr, "OnCalendar=*-*-* 00/6:00:00") || !strings.Contains(tmr, "Persistent=true") {
		t.Errorf("timer:\n%s", tmr)
	}
	for d, want := range map[time.Duration]string{time.Hour: "hourly", 24 * time.Hour: "daily", 168 * time.Hour: "weekly"} {
		if got := OnCalendar(d); got != want {
			t.Errorf("OnCalendar(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestCron(t *testing.T) {
	j := job
	j.ConfigDir = "/it's here"
	line := CronLine(j)
	want := `17 */6 * * * '/usr/local/bin/frost' backup --scheduled --config-dir '/it'\''s here' >> '/home/me/.cache/frost/frost.log' 2>&1 # frost-backup`
	if line != want {
		t.Errorf("cron line:\n got %s\nwant %s", line, want)
	}
	existing := "0 1 * * * other job\n" + line + "\n"
	if got := stripCron(existing); got != "0 1 * * * other job\n" {
		t.Errorf("stripCron = %q", got)
	}
	if got := stripCron(line + "\n"); got != "" {
		t.Errorf("stripCron of only frost = %q", got)
	}
}

func TestTaskArgs(t *testing.T) {
	got := strings.Join(TaskArgs(Job{Binary: `C:\frost\frost.exe`, Every: 24 * time.Hour}), " ")
	want := `/Create /F /TN frost backup /TR "C:\frost\frost.exe" backup --scheduled /SC DAILY /ST 03:17`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	got = strings.Join(TaskArgs(Job{Binary: `frost.exe`, Every: 4 * time.Hour}), " ")
	if !strings.HasSuffix(got, "/SC HOURLY /MO 4") {
		t.Errorf("4h task: %s", got)
	}
}
