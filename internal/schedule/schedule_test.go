package schedule

import (
	"encoding/binary"
	"encoding/xml"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

var job = Job{Binary: "/usr/local/bin/frost", Every: 6 * time.Hour, LogFile: "/home/me/.cache/frost/frost.log"}

func TestLaunchdPlistIsValidXML(t *testing.T) {
	j := job
	j.ConfigDir = "/tmp/a & b"
	j.CacheDir = "/tmp/cache & files"
	p := LaunchdPlist(j)
	if err := xml.Unmarshal([]byte(p), new(any)); err != nil {
		t.Fatalf("invalid plist: %v\n%s", err, p)
	}
	for _, want := range []string{"<integer>21600</integer>", "<string>--scheduled</string>", "/tmp/a &amp; b", "<string>--cache-dir</string>", "/tmp/cache &amp; files"} {
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

// stubLinger fakes logind with lingering on or off, and records each change
// frost asks for. With fail set, changing it fails, as when it needs a password.
func stubLinger(t *testing.T, on, fail bool) *[]bool {
	t.Helper()
	var calls []bool
	origLingering, origSetLinger := lingering, setLinger
	lingering = func() bool { return on }
	setLinger = func(v bool) error {
		calls = append(calls, v)
		if fail {
			return errors.New("interactive authentication required")
		}
		return nil
	}
	t.Cleanup(func() { lingering, setLinger = origLingering, origSetLinger })
	return &calls
}

func TestClaimLinger(t *testing.T) {
	marked := []byte("[Timer]\nOnCalendar=daily\n" + lingerMarker + "\n")
	for _, c := range []struct {
		name          string
		old           []byte
		on, fail      bool
		ours, enabled bool
		wantCalls     []bool
	}{
		{"turns it on", nil, false, false, true, true, []bool{true}},
		{"leaves the user's own lingering alone", nil, true, false, false, false, nil},
		{"carries on without it when that needs a password", nil, false, true, false, false, []bool{true}},
		{"keeps it when frost turned it on before", marked, true, false, true, false, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := stubLinger(t, c.on, c.fail)
			ours, enabled := claimLinger(c.old)
			if ours != c.ours || enabled != c.enabled || !slices.Equal(*calls, c.wantCalls) {
				t.Fatalf("claimLinger = %v, %v with calls %v, want %v, %v with %v", ours, enabled, *calls, c.ours, c.enabled, c.wantCalls)
			}
		})
	}
}

func TestReleaseLinger(t *testing.T) {
	_, tmr := SystemdUnits(job)
	calls := stubLinger(t, true, false)
	releaseLinger([]byte(tmr))
	if len(*calls) != 0 {
		t.Fatalf("turned off lingering frost didn't turn on: %v", *calls)
	}
	releaseLinger([]byte(tmr + lingerMarker + "\n"))
	if !slices.Equal(*calls, []bool{false}) {
		t.Fatalf("didn't turn off the lingering frost turned on: %v", *calls)
	}
	if !strings.HasPrefix(lingerMarker, "#") || strings.Contains(lingerMarker, "\n") {
		t.Fatalf("marker isn't a one-line unit file comment: %q", lingerMarker)
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

// task is the part of a Task Scheduler definition the tests check.
type task struct {
	Author      string `xml:"RegistrationInfo>Author"`
	Description string `xml:"RegistrationInfo>Description"`
	Calendar    *struct {
		Start  string    `xml:"StartBoundary"`
		Days   int       `xml:"ScheduleByDay>DaysInterval"`
		Weeks  int       `xml:"ScheduleByWeek>WeeksInterval"`
		Sunday *struct{} `xml:"ScheduleByWeek>DaysOfWeek>Sunday"`
	} `xml:"Triggers>CalendarTrigger"`
	Timer *struct {
		Start    string `xml:"StartBoundary"`
		Interval string `xml:"Repetition>Interval"`
	} `xml:"Triggers>TimeTrigger"`
	LogonType          string `xml:"Principals>Principal>LogonType"`
	RunLevel           string `xml:"Principals>Principal>RunLevel"`
	Hidden             bool   `xml:"Settings>Hidden"`
	StartWhenAvailable bool   `xml:"Settings>StartWhenAvailable"`
	Command            string `xml:"Actions>Exec>Command"`
	Arguments          string `xml:"Actions>Exec>Arguments"`
}

func parseTask(t *testing.T, s string) task {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(s))
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil } // declared UTF-16, written to disk as UTF-16
	var tk task
	if err := d.Decode(&tk); err != nil {
		t.Fatalf("invalid task XML: %v\n%s", err, s)
	}
	return tk
}

func TestTaskXML(t *testing.T) {
	now := time.Date(2026, 10, 4, 11, 18, 40, 0, time.Local)
	daily := parseTask(t, TaskXML(Job{Binary: `C:\frost\frost.exe`, Every: 24 * time.Hour}, now))
	if daily.Calendar == nil || daily.Calendar.Start != "2026-10-04T03:17:00" || daily.Calendar.Days != 1 || daily.Timer != nil {
		t.Errorf("daily trigger: %+v %+v", daily.Calendar, daily.Timer)
	}
	if daily.Command != `"C:\frost\frost.exe"` || daily.Arguments != "backup --scheduled" {
		t.Errorf("daily action: %q %q", daily.Command, daily.Arguments)
	}
	if daily.Author != "frost" || !strings.Contains(daily.Description, "Runs frost backup daily.") ||
		!strings.Contains(daily.Description, "frost config set schedule.enabled false") {
		t.Errorf("registration info: %q %q", daily.Author, daily.Description)
	}
	if daily.LogonType != "InteractiveToken" || daily.RunLevel != "LeastPrivilege" || daily.Hidden || daily.StartWhenAvailable {
		t.Errorf("principal or settings: %+v", daily)
	}

	weekly := parseTask(t, TaskXML(Job{Binary: `frost.exe`, Every: 7 * 24 * time.Hour}, now))
	if weekly.Calendar == nil || weekly.Calendar.Start != "2026-10-04T03:17:00" || weekly.Calendar.Weeks != 1 || weekly.Calendar.Sunday == nil {
		t.Errorf("weekly trigger: %+v", weekly.Calendar)
	}

	for every, interval := range map[time.Duration]string{time.Hour: "PT1H", 4 * time.Hour: "PT4H", 12 * time.Hour: "PT12H"} {
		tk := parseTask(t, TaskXML(Job{Binary: `frost.exe`, Every: every}, now))
		if tk.Timer == nil || tk.Timer.Start != "2026-10-04T11:18:00" || tk.Timer.Interval != interval || tk.Calendar != nil {
			t.Errorf("%v trigger: %+v %+v", every, tk.Timer, tk.Calendar)
		}
	}
}

func TestTaskXMLEscapesPaths(t *testing.T) {
	j := Job{Binary: `C:\program files\frost.exe`, ConfigDir: `C:\backup config\`, CacheDir: `C:\cache & <files>\`, LogFile: `C:\logs & 100% !\"frost".log`, Every: time.Hour}
	tk := parseTask(t, TaskXML(j, time.Now()))
	command, args := TaskCommand(j)
	if tk.Command != command || tk.Arguments != args {
		t.Fatalf("parsed %q %q, want %q %q", tk.Command, tk.Arguments, command, args)
	}
}

func TestTaskCommandIncludesLogPath(t *testing.T) {
	j := Job{Binary: `C:\program files\frost.exe`, ConfigDir: `C:\backup config\`, CacheDir: `C:\cache & files\`, LogFile: `C:\logs & 100% !\frost.log`, Every: time.Hour}
	command, args := TaskCommand(j)
	if command != `"C:\program files\frost.exe"` {
		t.Errorf("command = %q", command)
	}
	want := `backup --scheduled --config-dir "C:\backup config\\" --cache-dir "C:\cache & files\\" --log-file "C:\logs & 100% !\frost.log"`
	if args != want {
		t.Fatalf("arguments = %q, want %q", args, want)
	}
}

func TestUTF16File(t *testing.T) {
	b := utf16File("<a>é😀</a>")
	if len(b)%2 != 0 || b[0] != 0xff || b[1] != 0xfe {
		t.Fatalf("no UTF-16LE byte order mark: % x", b)
	}
	u := make([]uint16, len(b)/2-1)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2+2*i:])
	}
	if got := string(utf16.Decode(u)); got != "<a>é😀</a>" {
		t.Fatalf("decoded %q", got)
	}
}

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
	_, args := TaskCommand(Job{Binary: `C:\bin\frost.exe`, ConfigDir: `C:\backup config\`, Every: time.Hour})
	if !strings.Contains(args, `--config-dir "C:\backup config\\"`) {
		t.Fatal(args)
	}
}
