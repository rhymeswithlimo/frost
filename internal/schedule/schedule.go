// Package schedule installs automatic backups using the OS's own scheduler:
// launchd on macOS, a systemd user timer on Linux (cron if systemd isn't
// available), and Task Scheduler on Windows. frost never runs a daemon.
package schedule

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// Job describes the scheduled backup.
type Job struct {
	Binary    string        // absolute path to the frost binary
	Every     time.Duration // how often
	ConfigDir string        // passed as --config-dir when set
	CacheDir  string        // passed as --cache-dir when set
	LogFile   string        // where output goes
}

const (
	launchdLabel = "io.github.rhymeswithlimo.frost"
	systemdUnit  = "frost-backup"
	cronMarker   = "# frost-backup"
	taskName     = "frost backup"
)

// Kind names the scheduler used on this machine.
func Kind() string {
	switch runtime.GOOS {
	case "darwin":
		return "launchd"
	case "windows":
		return "Task Scheduler"
	default:
		if hasSystemd() {
			return "systemd"
		}
		return "cron"
	}
}

// Install creates or replaces the scheduled job.
func Install(j Job) error {
	for _, value := range []string{j.Binary, j.ConfigDir, j.CacheDir, j.LogFile} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("scheduler paths can't contain line breaks or NUL")
		}
	}
	if j.Every < time.Hour {
		return errors.New("schedule interval must be at least an hour")
	}
	switch Kind() {
	case "launchd":
		return installLaunchd(j)
	case "systemd":
		return installSystemd(j)
	case "cron":
		return installCron(j)
	default:
		return installTask(j)
	}
}

// Remove deletes the scheduled job if there is one.
func Remove() error {
	switch Kind() {
	case "launchd":
		p := launchdPath()
		exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid()), p).Run()
		return removeIfExists(p)
	case "systemd":
		exec.Command("systemctl", "--user", "disable", "--now", systemdUnit+".timer").Run()
		dir := systemdDir()
		err := errors.Join(removeIfExists(filepath.Join(dir, systemdUnit+".timer")),
			removeIfExists(filepath.Join(dir, systemdUnit+".service")))
		exec.Command("systemctl", "--user", "daemon-reload").Run()
		return err
	case "cron":
		cur, err := readCrontab()
		if err != nil {
			return err
		}
		return writeCrontab(stripCron(cur))
	default:
		out, err := exec.Command("schtasks", "/Delete", "/F", "/TN", taskName).CombinedOutput()
		if err != nil && !strings.Contains(strings.ToLower(string(out)), "cannot find") {
			return fmt.Errorf("schtasks: %s", strings.TrimSpace(string(out)))
		}
		return nil
	}
}

// Installed reports whether a scheduled job exists.
func Installed() bool {
	switch Kind() {
	case "launchd":
		return exists(launchdPath())
	case "systemd":
		return exists(filepath.Join(systemdDir(), systemdUnit+".timer"))
	case "cron":
		cur, _ := exec.Command("crontab", "-l").Output()
		return strings.Contains(string(cur), cronMarker)
	default:
		return exec.Command("schtasks", "/Query", "/TN", taskName).Run() == nil
	}
}

// ---- launchd ----

func launchdPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

var plistTmpl = template.Must(template.New("plist").Funcs(template.FuncMap{"xml": xmlEscape}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{xml .Binary}}</string>
		<string>backup</string>
		<string>--scheduled</string>
{{- if .ConfigDir}}
		<string>--config-dir</string>
		<string>{{xml .ConfigDir}}</string>
{{- end}}
{{- if .CacheDir}}
		<string>--cache-dir</string>
		<string>{{xml .CacheDir}}</string>
{{- end}}
	</array>
	<key>StartInterval</key>
	<integer>{{.Seconds}}</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>LowPriorityIO</key>
	<true/>
	<key>StandardOutPath</key>
	<string>{{xml .LogFile}}</string>
	<key>StandardErrorPath</key>
	<string>{{xml .LogFile}}</string>
</dict>
</plist>
`))

// LaunchdPlist renders the launchd agent for j.
func LaunchdPlist(j Job) string {
	var b bytes.Buffer
	plistTmpl.Execute(&b, struct {
		Job
		Label   string
		Seconds int
	}{j, launchdLabel, int(j.Every.Seconds())})
	return b.String()
}

func installLaunchd(j Job) error {
	p := launchdPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(LaunchdPlist(j)), 0o644); err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	exec.Command("launchctl", "bootout", domain, p).Run() // ignore: may not be loaded
	if out, err := exec.Command("launchctl", "bootstrap", domain, p).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ---- systemd ----

func hasSystemd() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	return exec.Command("systemctl", "--user", "show-environment").Run() == nil
}

func systemdDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "systemd", "user")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user")
}

// OnCalendar turns an interval into a systemd calendar expression.
func OnCalendar(d time.Duration) string {
	h := int(d.Hours())
	switch {
	case h >= 7*24:
		return "weekly"
	case h >= 24:
		return "daily"
	case h <= 1:
		return "hourly"
	default:
		return fmt.Sprintf("*-*-* 00/%d:00:00", h)
	}
}

// SystemdUnits renders the service and timer for j.
func SystemdUnits(j Job) (service, timer string) {
	quote := func(s string) string { return strconv.Quote(strings.NewReplacer("%", "%%", "$", "$$").Replace(s)) }
	cmd := quote(j.Binary) + " backup --scheduled"
	if j.ConfigDir != "" {
		cmd += " --config-dir " + quote(j.ConfigDir)
	}
	if j.CacheDir != "" {
		cmd += " --cache-dir " + quote(j.CacheDir)
	}
	service = "[Unit]\nDescription=frost backup\n\n[Service]\nType=oneshot\n" +
		"ExecStart=" + cmd + "\n" +
		"Nice=10\nIOSchedulingClass=idle\n"
	timer = "[Unit]\nDescription=Run frost backup " + humanEvery(j.Every) + "\n\n[Timer]\n" +
		"OnCalendar=" + OnCalendar(j.Every) + "\nPersistent=true\nRandomizedDelaySec=300\n\n" +
		"[Install]\nWantedBy=timers.target\n"
	return service, timer
}

func installSystemd(j Job) error {
	dir := systemdDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	svc, tmr := SystemdUnits(j)
	if err := os.WriteFile(filepath.Join(dir, systemdUnit+".service"), []byte(svc), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, systemdUnit+".timer"), []byte(tmr), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", systemdUnit + ".timer"}} {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %s", args[0], strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// ---- cron ----

// CronSpec turns an interval into a cron schedule.
func CronSpec(d time.Duration) string {
	h := int(d.Hours())
	switch {
	case h >= 7*24:
		return "17 3 * * 0"
	case h >= 24:
		return "17 3 * * *"
	case h <= 1:
		return "17 * * * *"
	default:
		return fmt.Sprintf("17 */%d * * *", h)
	}
}

// CronLine renders the crontab entry for j.
func CronLine(j Job) string {
	cmd := shellQuote(j.Binary) + " backup --scheduled"
	if j.ConfigDir != "" {
		cmd += " --config-dir " + shellQuote(j.ConfigDir)
	}
	if j.CacheDir != "" {
		cmd += " --cache-dir " + shellQuote(j.CacheDir)
	}
	command := cmd + " >> " + shellQuote(j.LogFile) + " 2>&1"
	return fmt.Sprintf("%s %s %s", CronSpec(j.Every), strings.ReplaceAll(command, "%", `\%`), cronMarker)
}

func stripCron(crontab string) string {
	var keep []string
	for l := range strings.SplitSeq(crontab, "\n") {
		if !strings.HasSuffix(strings.TrimSpace(l), " "+cronMarker) {
			keep = append(keep, l)
		}
	}
	if strings.TrimSpace(strings.Join(keep, "\n")) == "" {
		return ""
	}
	return strings.TrimRight(strings.Join(keep, "\n"), "\n") + "\n"
}

func installCron(j Job) error {
	cur, err := readCrontab()
	if err != nil {
		return err
	}
	return writeCrontab(stripCron(cur) + CronLine(j) + "\n")
}

func readCrontab() (string, error) {
	cmd := exec.Command("crontab", "-l")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && strings.HasPrefix(strings.TrimSpace(stderr.String()), "no crontab for ") {
			return "", nil
		}
		return "", fmt.Errorf("reading crontab: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func writeCrontab(content string) error {
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("crontab: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ---- Task Scheduler ----

// TaskArgs returns the schtasks arguments that create the job.
func TaskArgs(j Job) []string {
	run := windowsQuote(j.Binary) + " backup --scheduled"
	if j.ConfigDir != "" {
		run += " --config-dir " + windowsQuote(j.ConfigDir)
	}
	if j.CacheDir != "" {
		run += " --cache-dir " + windowsQuote(j.CacheDir)
	}
	if j.LogFile != "" {
		run += " --log-file " + windowsQuote(j.LogFile)
	}
	args := []string{"/Create", "/F", "/TN", taskName, "/TR", run}
	h := int(j.Every.Hours())
	switch {
	case h >= 7*24:
		return append(args, "/SC", "WEEKLY", "/ST", "03:17")
	case h >= 24:
		return append(args, "/SC", "DAILY", "/ST", "03:17")
	default:
		return append(args, "/SC", "HOURLY", "/MO", strconv.Itoa(max(h, 1)))
	}
}

// Windows doubles backslashes before quotes and before the closing quote.
func windowsQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range s {
		if r == '\\' {
			slashes++
			continue
		}
		if r == '"' {
			b.WriteString(strings.Repeat(`\`, slashes*2+1))
		} else {
			b.WriteString(strings.Repeat(`\`, slashes))
		}
		b.WriteRune(r)
		slashes = 0
	}
	b.WriteString(strings.Repeat(`\`, slashes*2))
	b.WriteByte('"')
	return b.String()
}

func installTask(j Job) error {
	if out, err := exec.Command("schtasks", TaskArgs(j)...).CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ---- helpers ----

func humanEvery(d time.Duration) string {
	switch h := int(d.Hours()); {
	case h >= 7*24:
		return "weekly"
	case h >= 24:
		return "daily"
	case h <= 1:
		return "hourly"
	default:
		return fmt.Sprintf("every %d hours", h)
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func xmlEscape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func removeIfExists(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
