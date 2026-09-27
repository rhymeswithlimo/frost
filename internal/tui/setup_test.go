package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
)

// fakeSetup is SetupDeps backed by a pretend storage: the Permafrost key
// "good" connects, anything else is refused.
type fakeSetup struct {
	state    RepoState
	existing *crypto.Key // the key the pretend repository was made with
	finished *config.Config
	newRepo  bool
	// what checkout in the browser brings back
	coToken string
	coErr   error
	coWaits int // how many times wait ran
}

func (f *fakeSetup) deps(local *crypto.Key) SetupDeps {
	return SetupDeps{
		LocalKey: local,
		Connect: func(_ context.Context, s config.Storage) (RepoState, error) {
			if s.Backend == "permafrost" && s.Permafrost.Token != "good" {
				return 0, errors.New("that access key wasn't accepted")
			}
			if s.Backend == "s3" && s.S3.Bucket == "nope" {
				return 0, &ConnectError{About: "bucket", Msg: "There's no bucket with that name."}
			}
			return f.state, nil
		},
		NewKey: crypto.NewKey,
		Unlock: func(_ context.Context, _ config.Storage, phrase string) (*crypto.Key, error) {
			k, err := crypto.KeyFromPhrase(phrase)
			if err != nil {
				return nil, err
			}
			if f.existing != nil && k.Fingerprint() != f.existing.Fingerprint() {
				return nil, errors.New("that phrase doesn't open these backups")
			}
			return k, nil
		},
		Finish: func(_ context.Context, cfg config.Config, _ *crypto.Key, newRepo bool) ([][2]string, error) {
			f.finished, f.newRepo = &cfg, newRepo
			return [][2]string{{"config", "~/.config/frost/config.toml"}}, nil
		},
		PickWords: func() (int, int) { return 2, 17 },
		DirExists: func(p string) bool { _, err := os.Stat(config.Expand(p)); return err == nil },
		Checkout: func(_ context.Context, _ config.Storage) (string, func() (string, error), error) {
			return "getfro.st/perma", func() (string, error) { f.coWaits++; return f.coToken, f.coErr }, nil
		},
	}
}

func typeText(t *testing.T, m tea.Model, s string) tea.Model {
	t.Helper()
	return step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
}

var up = tea.KeyMsg{Type: tea.KeyUp}

func sm(m tea.Model) setupModel { return m.(setupModel) }

// missingDir is a full path, on every OS, to a folder that doesn't exist:
// like a drive that isn't plugged in.
var missingDir = filepath.Join(os.TempDir(), "frost-setup-test-not-there", "Photos")

// walkNewSetup goes through a first-time setup on Permafrost, capturing
// every screen on the way.
func walkNewSetup(t *testing.T, w, h int) (map[string]tea.Model, *fakeSetup) {
	t.Helper()
	f := &fakeSetup{state: RepoNew}
	var m tea.Model = newSetup(context.Background(), f.deps(nil), config.Default(), false)
	m = step(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	shots := map[string]tea.Model{"01-welcome": m}

	m = step(t, m, key("enter"))
	shots["02-storage"] = m
	m = step(t, m, key("enter"))
	shots["02b-permafrost-choice"] = m
	m = step(t, m, key("enter"))
	shots["03-permafrost"] = m
	m = typeText(t, m, "bad-key-123")
	shots["03b-permafrost-typing"] = m
	m = step(t, m, key("enter")) // one question, so enter connects
	shots["04-connect-failed"] = m
	if sm(m).step != stDetails || !strings.Contains(sm(m).err, "wasn't accepted") {
		t.Fatalf("a refused key didn't stay on the question with the reason: step %d, err %q", sm(m).step, sm(m).err)
	}
	m = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = typeText(t, m, "good")
	m = step(t, m, key("enter"))
	if sm(m).step != stFolders {
		t.Fatalf("after connecting: step %d, err %q", sm(m).step, sm(m).err)
	}
	shots["05-folders"] = m
	m = typeText(t, m, missingDir)
	shots["06-folders-typing"] = m
	m = step(t, m, key("enter"))
	shots["07-folders-missing"] = m
	shots["07b-folders-choosing"] = step(t, m, up)
	m = step(t, m, key("enter")) // empty box: move on
	if sm(m).step != stSkip || len(sm(m).cfg.Exclude) == 0 {
		t.Fatalf("folders didn't go to the skip list with the defaults: step %d, %v", sm(m).step, sm(m).cfg.Exclude)
	}
	shots["07c-skip"] = m
	m = step(t, typeText(t, m, "*.iso"), key("enter"))
	shots["07d-skip-added"] = m
	shots["07e-skip-choosing"] = step(t, m, up)
	m = step(t, m, key("enter")) // empty box: move on
	shots["08-schedule"] = m
	m = step(t, m, up)
	m = step(t, m, key("enter"))
	shots["09-phrase"] = m
	m = step(t, m, key("enter"))
	if sm(m).step != stPhrase {
		t.Fatal("moved on without the words ever being shown")
	}
	shots["10-phrase-shown"] = step(t, m, key("v"))
	m = step(t, shots["10-phrase-shown"], key("enter"))
	shots["11-check"] = m
	m = typeText(t, m, "nope")
	m = step(t, m, key("enter"))
	m = typeText(t, m, "nope")
	m = step(t, m, key("enter"))
	shots["12-check-wrong"] = m
	words := strings.Fields(sm(m).key.Phrase())
	m = typeText(t, m, words[2])
	m = step(t, m, key("enter"))
	m = typeText(t, m, words[17])
	m = step(t, m, key("enter"))
	if sm(m).step != stReview {
		t.Fatalf("after the word check: step %d, err %q", sm(m).step, sm(m).err)
	}
	shots["13-review"] = m
	if m = step(t, m, key("enter")); sm(m).step != stReview || f.finished != nil {
		t.Fatal("enter saved on the review screen")
	}
	shots["13b-quit"] = step(t, m, key("q"))
	m = step(t, m, key("s"))
	shots["14-done"] = m
	return shots, f
}

// otherScreens are the ones a first-time setup doesn't pass through.
func otherScreens(t *testing.T, w, h int) map[string]tea.Model {
	t.Helper()
	local, _ := crypto.NewKey()
	f := &fakeSetup{state: RepoLocalWrong}
	cfg := config.Default()
	cfg.Storage.Backend, cfg.Storage.Permafrost.Token = "permafrost", "good"
	var m tea.Model = newSetup(context.Background(), f.deps(local), cfg, true)
	m = step(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	shots := map[string]tea.Model{"20-welcome-existing": m}
	m = step(t, m, key("enter"))
	shots["21-unlock"] = m
	shots["22-unlock-typing"] = typeText(t, m, "word word word")

	var b tea.Model = newSetup(context.Background(), f.deps(nil), config.Default(), false)
	b = step(t, b, tea.WindowSizeMsg{Width: w, Height: h})
	b = step(t, b, key("enter"))
	b = step(t, b, key("down"))
	shots["23-b2"] = step(t, b, key("enter"))

	c, _ := walkToChoice(t, w, h)
	shots["24-permafrost-no-key"] = c
	c, _ = c.Update(key("enter")) // without running the checkout
	shots["27-checkout-opening"] = c
	c, _ = c.Update(checkoutStartedMsg{id: sm(c).co.id, page: "getfro.st/perma", wait: nil, cancel: func() {}})
	shots["28-checkout-waiting"] = c
	f2 := &fakeSetup{state: RepoNew, coErr: errors.New("nothing came back from checkout within 25 minutes, so frost stopped waiting")}
	var d tea.Model = newSetup(context.Background(), f2.deps(nil), config.Default(), false)
	d = step(t, d, tea.WindowSizeMsg{Width: w, Height: h})
	d = step(t, step(t, step(t, d, key("enter")), key("enter")), key("down"))
	d = step(t, d, key("enter"))
	shots["29-checkout-failed"] = d
	return shots
}

// walkToChoice goes from the welcome to "I don't have a key yet", ready
// for enter to open the browser.
func walkToChoice(t *testing.T, w, h int) (tea.Model, *fakeSetup) {
	t.Helper()
	f := &fakeSetup{state: RepoNew}
	var m tea.Model = newSetup(context.Background(), f.deps(nil), config.Default(), false)
	m = step(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	m = step(t, m, key("enter")) // welcome
	m = step(t, m, key("enter")) // Permafrost
	m = step(t, m, key("down"))  // I don't have a key yet
	if sm(m).step != stPermaChoice || sm(m).permaCur != 1 {
		t.Fatalf("didn't reach the have-a-key choice: step %d", sm(m).step)
	}
	return m, f
}

func TestSetupCheckout(t *testing.T) {
	m, f := walkToChoice(t, 80, 24)

	// Checkout fails: say why, and offer a retry or pasting.
	f.coErr = errors.New("checkout was cancelled in the browser")
	m = step(t, m, key("enter"))
	if sm(m).step != stCheckout || sm(m).co.waiting || !strings.Contains(stripANSI(m.View()), "cancelled") {
		t.Fatalf("failed checkout: step %d, waiting %v", sm(m).step, sm(m).co.waiting)
	}
	paste := step(t, m, key("p"))
	if sm(paste).step != stDetails || sm(paste).prov != 0 {
		t.Fatalf("[p] didn't go to pasting a key: step %d", sm(paste).step)
	}
	if back := step(t, paste, key("esc")); sm(back).step != stPermaChoice {
		t.Errorf("esc from pasting went to step %d, want the have-a-key choice", sm(back).step)
	}

	// Retry, and this time a key comes back: it's used to connect.
	f.coErr, f.coToken = nil, "good"
	m = step(t, m, key("r"))
	if sm(m).step != stFolders || sm(m).cfg.Storage.Permafrost.Token != "good" || sm(m).cfg.Storage.Backend != "permafrost" {
		t.Fatalf("after a successful checkout: step %d, storage %+v, err %q", sm(m).step, sm(m).cfg.Storage, sm(m).err)
	}

	// A key that doesn't connect lands on the paste screen with it filled in.
	m, f = walkToChoice(t, 80, 24)
	f.coToken = "expired"
	m = step(t, m, key("enter"))
	if sm(m).step != stDetails || sm(m).details.fields[0].value != "expired" || sm(m).err == "" {
		t.Fatalf("bad new key: step %d, value %q, err %q", sm(m).step, sm(m).details.fields[0].value, sm(m).err)
	}

	// Cancelled while the browser was still opening: wait still runs, so
	// the listener closes instead of telling a paying browser all's well.
	m, f = walkToChoice(t, 80, 24)
	m, _ = m.Update(key("enter"))
	opening := sm(m).co.id
	m = step(t, m, key("esc"))
	m = step(t, m, checkoutStartedMsg{id: opening, page: "x", wait: func() (string, error) { f.coWaits++; return "", context.Canceled }, cancel: func() {}})
	if f.coWaits != 1 || sm(m).step != stPermaChoice {
		t.Errorf("late start after cancel: waits %d, step %d", f.coWaits, sm(m).step)
	}

	// A result from a checkout that was cancelled is ignored.
	m, _ = walkToChoice(t, 80, 24)
	m, _ = m.Update(key("enter"))
	old := sm(m).co.id
	m = step(t, m, key("esc"))
	m = step(t, m, checkoutMsg{id: old, token: "good"})
	if sm(m).step != stPermaChoice || sm(m).cfg.Storage.Permafrost.Token != "" {
		t.Errorf("a cancelled checkout's key was used: step %d", sm(m).step)
	}
}

// A key from an earlier run that was quit is filled in.
func TestSetupEarlierKeyFilledIn(t *testing.T) {
	cfg := config.Default()
	cfg.Storage.Backend, cfg.Storage.S3.Bucket = "s3", "backups"
	cfg.Storage.Permafrost.Token = "earlier"
	var m tea.Model = newSetup(context.Background(), (&fakeSetup{}).deps(nil), cfg, false)
	m = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = step(t, m, key("enter"))
	for sm(m).provCur > 0 {
		m = step(t, m, tea.KeyMsg{Type: tea.KeyUp})
	}
	m = step(t, step(t, m, key("enter")), key("enter"))
	if sm(m).step != stDetails || sm(m).details.fields[0].value != "earlier" {
		t.Fatalf("step %d, key %q", sm(m).step, sm(m).details.fields[0].value)
	}
}

func TestSetupScreens(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	dump := os.Getenv("FROST_TUI_DUMP")
	for _, size := range [][2]int{{120, 40}, {80, 24}, {56, 18}, {40, 12}, {1, 1}} {
		if size[0] < minW || size[1] < minH {
			// Too small for the card: every screen is the resize notice,
			// but still exactly the window's size.
			f := &fakeSetup{}
			var m tea.Model = newSetup(context.Background(), f.deps(nil), config.Default(), false)
			m = step(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			checkFrame(t, "tiny", m.View(), size)
			continue
		}
		shots, f := walkNewSetup(t, size[0], size[1])
		for name, s := range otherScreens(t, size[0], size[1]) {
			shots[name] = s
		}
		for name, s := range shots {
			v := s.View()
			checkFrame(t, fmt.Sprintf("%dx%d %s", size[0], size[1], name), v, size)
			plain := stripANSI(v)
			for _, secret := range []string{"bad-key-123", "good"} {
				// "good" is also a phrase word, so skip the screen showing the words.
				if strings.Contains(plain, secret) && name != "10-phrase-shown" {
					t.Errorf("%dx%d %s: shows the access key %q", size[0], size[1], name, secret)
				}
			}
			if name != "14-done" && name != "13b-quit" && !strings.Contains(lastLine(v), "quit") {
				t.Errorf("%dx%d %s: footer lost quit: %q", size[0], size[1], name, lastLine(v))
			}
			hasMark := strings.Contains(plain, "▒")
			if name == "01-welcome" && size[0] >= 80 && !hasMark {
				t.Errorf("%dx%d welcome: no wordmark", size[0], size[1])
			}
			if !strings.Contains(name, "welcome") && hasMark {
				t.Errorf("%dx%d %s: wordmark outside the welcome screen", size[0], size[1], name)
			}
			if dump != "" {
				os.WriteFile(filepath.Join(dump, fmt.Sprintf("setup-%s-%03d.ans", name, size[0])), []byte(v), 0o644)
			}
		}
		if f.finished == nil || !f.newRepo {
			t.Fatalf("%dx%d: setup didn't finish with a new repository", size[0], size[1])
		}
		if got := f.finished.Paths; len(got) != 1 || got[0] != missingDir {
			t.Errorf("paths = %v", got)
		}
		if got := f.finished.Exclude; len(got) != len(config.Default().Exclude)+1 || got[len(got)-1] != "*.iso" {
			t.Errorf("exclude = %v", got)
		}
		if f.finished.Storage.Permafrost.Token != "good" || f.finished.Storage.Permafrost.URL != "" {
			t.Errorf("storage = %+v", f.finished.Storage.Permafrost)
		}
		if f.finished.Schedule.Every != "12h" {
			t.Errorf("schedule = %+v", f.finished.Schedule)
		}
	}
}

func checkFrame(t *testing.T, name, v string, size [2]int) {
	t.Helper()
	if got := lipgloss.Height(v); got != size[1] {
		t.Errorf("%s: height %d, want %d", name, got, size[1])
	}
	if got := lipgloss.Width(v); got != size[0] {
		t.Errorf("%s: width %d, want %d", name, got, size[0])
	}
	if i := strings.IndexFunc(v, func(r rune) bool { return r < 0x20 && r != '\n' && r != 0x1b }); i >= 0 {
		t.Errorf("%s: control character %q in the frame", name, v[i])
	}
}

func TestSetupPhraseHiddenUntilAsked(t *testing.T) {
	shots, _ := walkNewSetup(t, 80, 24)
	words := strings.Fields(sm(shots["09-phrase"]).key.Phrase())
	covered := stripANSI(shots["09-phrase"].View())
	shown := stripANSI(shots["10-phrase-shown"].View())
	check := stripANSI(shots["11-check"].View())
	for _, w := range words {
		if !strings.Contains(shown, w) {
			t.Errorf("[v] didn't show %q", w)
		}
	}
	// Short words can turn up inside other text, so look for all of them.
	if n := countWords(covered, words); n == len(words) {
		t.Error("the phrase is readable before [v]")
	}
	if n := countWords(check, words); n == len(words) {
		t.Error("the check screen shows the phrase")
	}
}

func countWords(s string, words []string) int {
	n := 0
	for _, w := range words {
		if strings.Contains(s, " "+w+" ") {
			n++
		}
	}
	return n
}

func TestSetupExistingRepoAsksForPhrase(t *testing.T) {
	old, _ := crypto.NewKey()
	f := &fakeSetup{state: RepoNeedsPhrase, existing: old}
	cfg := config.Default()
	cfg.Storage.Backend, cfg.Storage.Permafrost.Token = "permafrost", "good"

	// A saved config: the welcome connects on its own and heads for the key.
	var m tea.Model = newSetup(context.Background(), f.deps(nil), cfg, true)
	m = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = step(t, m, key("enter"))
	if sm(m).step != stUnlock {
		t.Fatalf("step %d, err %q", sm(m).step, sm(m).err)
	}
	other, _ := crypto.NewKey()
	m = typeText(t, m, other.Phrase())
	if first := strings.Fields(other.Phrase())[0]; strings.Contains(stripANSI(m.View()), other.Phrase()[:20]) {
		t.Errorf("the phrase shows as it's typed (%s...)", first)
	}
	if v := stripANSI(step(t, m, tea.KeyMsg{Type: tea.KeyTab}).View()); !strings.Contains(v, strings.Fields(other.Phrase())[23]) {
		t.Error("[tab] didn't show the phrase")
	}
	m = step(t, m, key("enter"))
	if sm(m).step != stUnlock || sm(m).err == "" {
		t.Fatal("the wrong phrase was accepted")
	}
	m = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = typeText(t, m, old.Phrase())
	m = step(t, m, key("enter"))
	if sm(m).step != stReview {
		t.Fatalf("step %d, err %q", sm(m).step, sm(m).err)
	}
	step(t, m, key("s"))
	if f.finished == nil || f.newRepo {
		t.Fatal("saved as a new repository")
	}
}

func TestSetupWrongLocalKeyOffersPhrase(t *testing.T) {
	local, _ := crypto.NewKey()
	f := &fakeSetup{state: RepoLocalWrong}
	var m tea.Model = newSetup(context.Background(), f.deps(local), config.Default(), false)
	m = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = step(t, m, key("enter"))
	m = step(t, m, key("enter"))
	m = step(t, m, key("enter")) // I have a key
	m = typeText(t, m, "good")
	m = step(t, m, key("enter"))
	m = typeText(t, m, t.TempDir())
	m = step(t, m, key("enter")) // add the folder
	m = step(t, m, key("enter")) // the empty box moves on
	m = step(t, m, key("enter")) // keep the skip list
	m = step(t, m, key("enter")) // schedule
	if sm(m).step != stUnlock || !strings.Contains(stripANSI(m.View()), "different key") {
		t.Fatalf("step %d, err %q", sm(m).step, sm(m).err)
	}
}

func TestSetupSavedConfigGoesToReview(t *testing.T) {
	local, _ := crypto.NewKey()
	f := &fakeSetup{state: RepoLocalOK}
	cfg := config.Default()
	cfg.Storage.Backend = "s3"
	cfg.Storage.S3 = config.S3{Endpoint: "s3.us-west-004.backblazeb2.com", Region: "us-west-004", Bucket: "mine", AccessKeyID: "id", SecretAccessKey: "shh-secret"}
	var m tea.Model = newSetup(context.Background(), f.deps(local), cfg, true)
	m = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = step(t, m, key("enter"))
	if sm(m).step != stReview {
		t.Fatalf("step %d, err %q", sm(m).step, sm(m).err)
	}
	if v := stripANSI(m.View()); !strings.Contains(v, "Backblaze B2, bucket mine") {
		t.Fatalf("review doesn't describe the storage:\n%s", v)
	}

	// Edit the schedule from the review and come back to it.
	m = step(t, m, key("down"))
	m = step(t, m, key("down"))
	m = step(t, m, key("down"))
	m = step(t, m, key("e"))
	if sm(m).step != stSchedule {
		t.Fatalf("[e] on schedule went to step %d", sm(m).step)
	}
	m = step(t, m, key("down"))
	m = step(t, m, key("enter"))
	if sm(m).step != stReview {
		t.Fatalf("after editing: step %d", sm(m).step)
	}

	// The B2 form comes back filled in, secret hidden.
	m = step(t, m, up)
	m = step(t, m, up)
	m = step(t, m, up)
	m = step(t, m, key("e"))
	m = step(t, m, key("enter"))
	v := stripANSI(m.View())
	if sm(m).step != stDetails || !strings.Contains(v, "west-004.backblazeb2.com") || strings.Contains(v, "shh-secret") {
		t.Fatalf("B2 form:\n%s", v)
	}
	for range 4 { // endpoint, bucket, key ID, app key: the folder is behind [tab]
		m = step(t, m, key("enter"))
	}
	if sm(m).step != stReview {
		t.Fatalf("after reconnecting: step %d, err %q", sm(m).step, sm(m).err)
	}
	step(t, m, key("s"))
	if f.finished == nil || f.finished.Storage.S3.SecretAccessKey != "shh-secret" || f.finished.Storage.S3.Region != "us-west-004" {
		t.Fatalf("saved %+v", f.finished)
	}
}

func TestProviderEndpoints(t *testing.T) {
	cases := []struct {
		prov             int
		values           []string
		endpoint, region string
	}{
		{1, []string{"https://s3.eu-central-003.backblazeb2.com", "b", "id", "sec", ""}, "https://s3.eu-central-003.backblazeb2.com", "eu-central-003"},
		{2, []string{"eu-west-2", "b", "id", "sec", ""}, "s3.eu-west-2.amazonaws.com", "eu-west-2"},
		{3, []string{"abc123", "b", "id", "sec", ""}, "abc123.r2.cloudflarestorage.com", "auto"},
		{4, []string{"us-east-2", "b", "id", "sec", ""}, "s3.us-east-2.wasabisys.com", "us-east-2"},
	}
	for _, c := range cases {
		var s config.Storage
		providers[c.prov].apply(c.values, &s)
		if s.S3.Endpoint != c.endpoint || s.S3.Region != c.region || s.Backend != "s3" {
			t.Errorf("%s: got %q %q", providers[c.prov].name, s.S3.Endpoint, s.S3.Region)
		}
		if got := matchProvider(s); got != c.prov {
			t.Errorf("%s: matched as %s", providers[c.prov].name, providers[got].name)
		}
		if back := providers[c.prov].read(s); back[0] != c.values[0] {
			t.Errorf("%s: read back %q, want %q", providers[c.prov].name, back[0], c.values[0])
		}
	}
}

// errText is the error the screen shows, or "".
func errText(m tea.Model) string { return sm(m).err }

func TestSetupFolderRules(t *testing.T) {
	dir := t.TempDir()
	docs := filepath.Join(dir, "Documents")
	os.MkdirAll(filepath.Join(docs, "taxes"), 0o755)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)

	f := &fakeSetup{state: RepoNew}
	var m tea.Model = newSetup(context.Background(), f.deps(nil), config.Default(), false)
	m = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = step(t, m, key("enter"))
	m = step(t, m, key("enter"))
	m = step(t, m, key("enter")) // I have a key
	m = typeText(t, m, "good")
	m = step(t, m, key("enter"))
	if sm(m).step != stFolders || len(sm(m).cfg.Paths) != 0 {
		t.Fatalf("folders should start empty: step %d, paths %v", sm(m).step, sm(m).cfg.Paths)
	}
	add := func(p string) tea.Model {
		return step(t, typeText(t, m, p), key("enter"))
	}
	if m = step(t, m, key("enter")); !strings.Contains(errText(m), "at least one folder") || sm(m).step != stFolders {
		t.Fatalf("moved on with no folders: %q", errText(m))
	}
	for p, want := range map[string]string{
		"Documents":                     "full path",
		filepath.Join(dir, "notes.txt"): "a file",
		filepath.Join(docs, "taxes"):    "", // fine on its own
	} {
		got := add(p)
		if want == "" && errText(got) != "" || want != "" && !strings.Contains(errText(got), want) {
			t.Errorf("adding %q: error %q, want %q", p, errText(got), want)
		}
	}
	m = add(filepath.Join(docs, "taxes"))
	m = add(docs) // takes over taxes, which is inside it
	if got := sm(m).cfg.Paths; len(got) != 1 || got[0] != docs || sm(m).note == "" {
		t.Fatalf("paths %v, note %q", got, sm(m).note)
	}
	if got := add(docs + "/"); !strings.Contains(errText(got), "already on the list") {
		t.Errorf("duplicate with a slash: %q", errText(got))
	}
	if got := add(filepath.Join(docs, "taxes")); !strings.Contains(errText(got), "inside") {
		t.Errorf("folder inside one on the list: %q", errText(got))
	}
	// The message goes with the next key press.
	if got := step(t, add("Documents"), key("x")); errText(got) != "" {
		t.Errorf("error outlived a key press: %q", errText(got))
	}
}

func TestSetupStorageQuestions(t *testing.T) {
	f := &fakeSetup{state: RepoNew}
	var m tea.Model = newSetup(context.Background(), f.deps(nil), config.Default(), false)
	m = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = step(t, m, key("enter"))
	m = step(t, m, key("down"))
	m = step(t, m, key("down")) // Amazon S3
	m = step(t, m, key("enter"))

	answer := func(v string) tea.Model {
		m = step(t, typeText(t, m, v), key("enter"))
		return m
	}
	if answer(""); !strings.Contains(errText(m), "Type the region") || sm(m).details.focus != 0 {
		t.Fatalf("empty answer: focus %d, %q", sm(m).details.focus, errText(m))
	}
	if answer("US East"); !strings.Contains(errText(m), "us-east-1") || sm(m).details.focus != 0 {
		t.Fatalf("bad region got through: %q", errText(m))
	}
	m = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	answer("us-east-1")
	answer("nope")
	answer("AKIAEXAMPLE")
	v := stripANSI(typeText(t, m, "shh").View())
	if strings.Contains(v, "shh") {
		t.Error("the secret shows as it's typed")
	}
	if v := stripANSI(step(t, typeText(t, m, "shh"), tea.KeyMsg{Type: tea.KeyTab}).View()); !strings.Contains(v, "shh") {
		t.Error("[tab] didn't show the secret")
	}
	answer("shh")
	// The bucket doesn't exist: back to the bucket question, answers kept.
	if sm(m).step != stDetails || sm(m).details.focus != 1 || !strings.Contains(errText(m), "no bucket") {
		t.Fatalf("after a bucket error: step %d, focus %d, %q", sm(m).step, sm(m).details.focus, errText(m))
	}
	if d := sm(m).details; d.values()[0] != "us-east-1" || d.values()[3] != "shh" {
		t.Fatalf("answers lost: %v", d.values())
	}
}

func TestSetupCheckAndUnlockErrors(t *testing.T) {
	shots, _ := walkNewSetup(t, 80, 24)
	m := shots["11-check"]
	m = step(t, typeText(t, m, "wrong"), key("enter"))
	if sm(m).check.focus != 0 || !strings.Contains(errText(m), "not word 3") {
		t.Fatalf("a wrong word moved on: focus %d, %q", sm(m).check.focus, errText(m))
	}
	if m = step(t, m, key("enter")); !strings.Contains(errText(m), "Type word 3") {
		t.Errorf("empty word: %q", errText(m))
	}
	// esc goes back to the words from the second question too.
	words := strings.Fields(sm(m).key.Phrase())
	m = step(t, typeText(t, m, words[2]), key("enter"))
	if sm(m).check.focus != 1 {
		t.Fatalf("didn't move to the second word")
	}
	if m = step(t, m, key("esc")); sm(m).step != stPhrase {
		t.Errorf("esc on the second word went to step %d, not the words", sm(m).step)
	}

	f := &fakeSetup{state: RepoNeedsPhrase}
	cfg := config.Default()
	cfg.Storage.Backend, cfg.Storage.Permafrost.Token = "permafrost", "good"
	var u tea.Model = newSetup(context.Background(), f.deps(nil), cfg, true)
	u = step(t, u, tea.WindowSizeMsg{Width: 80, Height: 24})
	u = step(t, u, key("enter"))
	u = step(t, typeText(t, u, "one two three"), key("enter"))
	if !strings.Contains(errText(u), "3 words") || sm(u).step != stUnlock {
		t.Fatalf("short phrase: %q", errText(u))
	}
}

func TestSetupSkipPatterns(t *testing.T) {
	local, _ := crypto.NewKey()
	f := &fakeSetup{state: RepoLocalOK}
	cfg := config.Default()
	cfg.Paths = []string{"/tmp"}
	cfg.Storage.Backend, cfg.Storage.Permafrost.Token = "permafrost", "good"
	var m tea.Model = newSetup(context.Background(), f.deps(local), cfg, true)
	m = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = step(t, m, key("enter"))
	m = step(t, m, key("down"))
	m = step(t, m, key("down"))
	m = step(t, m, key("e"))
	if sm(m).step != stSkip {
		t.Fatalf("step %d", sm(m).step)
	}
	if m = step(t, typeText(t, m, "*.raw, [abc"), key("enter")); !strings.Contains(errText(m), "valid pattern") {
		t.Fatalf("bad pattern: %q", errText(m))
	}
	if got := sm(m).cfg.Exclude; len(got) != len(cfg.Exclude) {
		t.Fatalf("a bad pattern added some anyway: %v", got)
	}
	m = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = step(t, typeText(t, m, "*.raw, *.tmp, *.iso"), key("enter")) // *.tmp is a default
	want := append(append([]string{}, cfg.Exclude...), "*.raw", "*.iso")
	if got := sm(m).cfg.Exclude; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("exclude = %v, want %v", got, want)
	}
	// Up picks the last one, x removes it.
	m = step(t, step(t, m, up), key("x"))
	if got := sm(m).cfg.Exclude; got[len(got)-1] != "*.raw" {
		t.Fatalf("after removing: %v", got)
	}
	m = step(t, step(t, m, key("esc")), key("enter"))
	if sm(m).step != stReview {
		t.Fatalf("empty box didn't go back to the review: step %d", sm(m).step)
	}
}

func TestSetupQuitAsksFirst(t *testing.T) {
	f := &fakeSetup{state: RepoNew}
	var m tea.Model = newSetup(context.Background(), f.deps(nil), config.Default(), false)
	m = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if _, cmd := m.Update(key("q")); cmd == nil {
		t.Fatal("q on the welcome screen should quit straight away")
	}
	m = step(t, m, key("enter")) // storage
	m, cmd := m.Update(key("q"))
	if cmd != nil || !sm(m).quit || !strings.Contains(stripANSI(m.View()), "Are you sure you want to quit?") {
		t.Fatal("q quit without asking")
	}
	if m = step(t, m, key("enter")); !sm(m).quit || sm(m).step != stStorage {
		t.Fatal("other keys should do nothing while asking")
	}
	if m = step(t, m, key("n")); sm(m).quit || sm(m).step != stStorage {
		t.Fatal("[n] didn't go back to setup")
	}
	m = step(t, m, key("q"))
	if _, cmd := m.Update(key("y")); cmd == nil {
		t.Fatal("[y] didn't quit")
	}
}

func TestInputCursor(t *testing.T) {
	var f form
	f.fields = []field{{}}
	press := func(keys ...tea.KeyMsg) {
		for _, k := range keys {
			f.key(k)
		}
	}
	k := func(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }
	runes := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

	press(runes("~/Pictres"))
	press(k(tea.KeyLeft), k(tea.KeyLeft), k(tea.KeyLeft), runes("u"))
	if got := f.fields[0].value; got != "~/Pictures" {
		t.Fatalf("insert in the middle: %q", got)
	}
	press(k(tea.KeyHome), k(tea.KeyDelete), k(tea.KeyDelete), k(tea.KeyEnd), k(tea.KeyBackspace))
	if got := f.fields[0].value; got != "Picture" {
		t.Fatalf("home, delete, end, backspace: %q", got)
	}
	press(runes(" two three"), k(tea.KeyCtrlLeft), k(tea.KeyCtrlW))
	if got := f.fields[0].value; got != "Picture three" {
		t.Fatalf("word jump and delete: %q", got)
	}
	press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b"), Alt: true})
	if got, at := f.fields[0].value, f.fields[0].cursor(); got != "Picture three" || at != 0 {
		t.Fatalf("alt+b: %q, cursor %d", got, at)
	}
	// Setting the value directly leaves the cursor at the end.
	f.fields[0].value, f.fields[0].back = "abc", 0
	if f.fields[0].cursor() != 3 {
		t.Fatal("cursor isn't at the end")
	}

	// The cursor stays in view in a long answer.
	long := field{value: strings.Repeat("a", 100) + "XYZ" + strings.Repeat("b", 100), back: 101}
	if v := stripANSI(inputText(long, false, true, 20)); !strings.Contains(v, "Z") || lipgloss.Width(v) > 20 {
		t.Fatalf("cursor scrolled out of view: %q", v)
	}
}

func TestSetupDoneScreen(t *testing.T) {
	shots, _ := walkNewSetup(t, 80, 24)
	v := stripANSI(shots["14-done"].View())
	for _, want := range []string{"All set up!", "every 12 hours", "frost backup", "frost browse"} {
		if !strings.Contains(v, want) {
			t.Errorf("done screen is missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "setup") {
		t.Error("done screen still has the setup header")
	}
}
