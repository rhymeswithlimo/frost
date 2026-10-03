package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
)

func TestSetupPhraseFeedbackFits(t *testing.T) {
	k, err := crypto.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	m := newSetup(context.Background(), SetupDeps{}, config.Default(), false)
	m.w, m.h, m.step, m.key = minW, minH, stPhrase, k
	m.err = "Press [v] to show the words, and write them down first."
	if !strings.Contains(stripANSI(m.View()), "Press [v]") {
		t.Fatal("phrase feedback is clipped at the minimum setup size")
	}
}

func TestSetupFeedbackWithNoRowsDoesNotAddBlankLine(t *testing.T) {
	m := newSetup(context.Background(), SetupDeps{}, config.Default(), false)
	m.err = "The answer already fills the available rows."
	p := page{question: "Question", body: []string{fill(52, 2)}}
	if got := lipgloss.Height(m.render(p, 52, 3)); got != 3 {
		t.Fatalf("an empty feedback slice added a row: got %d, want 3", got)
	}
}

func TestSetupSingleRowFeedbackKeepsEveryCharacter(t *testing.T) {
	lines := []string{"日本語👩‍💻", strings.Repeat("x", 52), "Diagnostic end."}
	text := strings.Join(lines, "\n")
	for top, want := range lines {
		if got := sliceSetupLines(text, 1, top); got != want {
			t.Fatalf("single-row feedback changed diagnostic row %d", top)
		}
	}
	if got := sliceSetupLines(text, 1, 100); got != lines[len(lines)-1] {
		t.Fatal("single-row feedback did not clamp at the end")
	}
}

func TestSetupLongFeedbackKeepsAnswersVisible(t *testing.T) {
	for _, size := range [][2]int{{minW, minH}, {80, 24}} {
		for _, st := range []setupStep{stFolders, stSkip} {
			t.Run(fmt.Sprintf("%dx%d/%d", size[0], size[1], st), func(t *testing.T) {
				m := newSetup(context.Background(), SetupDeps{}, config.Default(), false)
				m.w, m.h, m.step = size[0], size[1], st
				items := make([]string, 100)
				for i := range items {
					items[i] = fmt.Sprintf("item-%03d", i)
				}
				m.cfg.Paths, m.cfg.Exclude = items, items
				m.folderSel, m.skipSel = 50, 50
				m.folderIn.fields[0].value = "answer-stays-visible"
				m.skipIn.fields[0].value = "answer-stays-visible"
				m.err = "Cannot use that answer. " + strings.Repeat("A detailed explanation follows. ", 100)
				w, h := min(cardW, m.w-4), min(cardH, m.h-2)-5
				body := m.render(m.page(w, h), w, h)
				if lipgloss.Height(body) > h {
					t.Fatalf("setup body has %d rows, only %d fit", lipgloss.Height(body), h)
				}
				v := stripANSI(m.View())
				for _, want := range []string{"item-050", "answer-stays-visible", "Cannot use that answer.", "[pgup pgdn] details"} {
					if !strings.Contains(v, want) {
						t.Errorf("setup lost %q with a long error", want)
					}
				}
				checkFrame(t, "long feedback", m.View(), size)
			})
		}
	}
}

func TestSetupListRowsStayWithinBudget(t *testing.T) {
	items := make([]string, 100)
	for i := range items {
		items[i] = fmt.Sprintf("item-%03d", i)
	}
	for n := 1; n <= listMax; n++ {
		for _, sel := range []int{-1, 0, 50, 99} {
			rows := listRows(items, sel, n, 52, func(i int, on bool) string { return items[i] })
			if len(rows) > n {
				t.Errorf("%d-row budget shows %d rows at selection %d", n, len(rows), sel)
			}
			at := sel
			if at < 0 {
				at = len(items) - 1
			}
			if !strings.Contains(strings.Join(rows, "\n"), items[at]) {
				t.Errorf("%d-row budget hides selection %d", n, sel)
			}
		}
	}
}

func TestSetupLongScheduleFailureKeepsNextCommands(t *testing.T) {
	for _, size := range [][2]int{{minW, minH}, {80, 24}, {50, 20}} {
		m := newSetup(context.Background(), SetupDeps{}, config.Default(), false)
		m.w, m.h, m.step = size[0], size[1], stDone
		m.savedRows = [][2]string{{"schedule", "not installed: " + strings.Repeat("scheduler reported a detailed problem; ", 100)}}
		checkFrame(t, "long schedule failure", m.View(), size)
		if size[0] < minW || size[1] < minH {
			continue
		}
		v := stripANSI(m.View())
		for _, want := range []string{"couldn't be set up", "...", "Run frost init again to retry.", "frost backup", "frost browse", "frost status", "[enter] exit"} {
			if !strings.Contains(v, want) {
				t.Errorf("%dx%d schedule failure hides %q", size[0], size[1], want)
			}
		}
	}
}

func TestSetupLongCheckoutFailureKeepsChoices(t *testing.T) {
	m := newSetup(context.Background(), SetupDeps{}, config.Default(), false)
	m.w, m.h, m.step = minW, minH, stCheckout
	m.co.failed = strings.Repeat("The browser reported a detailed problem; ", 100)
	v := stripANSI(m.View())
	for _, want := range []string{"[pgup pgdn] details", "[r] try again", "[p] paste a key instead"} {
		if !strings.Contains(v, want) {
			t.Errorf("long checkout failure hides %q", want)
		}
	}
}

func TestSetupFeedbackPagingPreservesDiagnosticsAndAnswer(t *testing.T) {
	for _, size := range [][2]int{{minW, minH}, {80, 24}} {
		m := newSetup(context.Background(), SetupDeps{}, config.Default(), false)
		m.w, m.h, m.step = size[0], size[1], stFolders
		m.cfg.Paths = []string{"selected-folder"}
		m.folderIn.fields[0].value = "answer-stays-visible"
		m.err = "Error begins. " + strings.Repeat("Read the next diagnostic line. ", 30) + "ERROR-END."
		m.note = "Note begins. " + strings.Repeat("Read the next diagnostic line. ", 30) + "NOTE-END."
		wantErr, wantNote := m.err, m.note
		if !strings.Contains(stripANSI(lastLine(m.View())), "[pgup pgdn] details") {
			t.Fatal("oversized feedback has no paging hint")
		}
		seenErrorEnd, seenNoteEnd := false, false
		for i := 0; i < 100; i++ {
			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
			if cmd != nil {
				t.Fatal("paging diagnostic triggered an action")
			}
			m = next.(setupModel)
			if m.err != wantErr || m.note != wantNote || m.folderIn.fields[0].value != "answer-stays-visible" || m.folderSel != -1 {
				t.Fatal("paging changed feedback or the answer")
			}
			v := stripANSI(m.View())
			seenErrorEnd = seenErrorEnd || strings.Contains(v, "ERROR-END.")
			seenNoteEnd = seenNoteEnd || strings.Contains(v, "NOTE-END.")
			if !strings.Contains(v, "selected-folder") || !strings.Contains(v, "answer-stays-visible") {
				t.Fatal("paging hid the folder or input")
			}
			checkFrame(t, "paged feedback", m.View(), size)
			_, maxTop := m.feedbackWindow()
			if m.feedbackTop == maxTop {
				break
			}
		}
		if !seenErrorEnd || !seenNoteEnd {
			t.Fatal("paging never reached the error and note tails")
		}
		for m.feedbackTop > 0 {
			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
			if cmd != nil {
				t.Fatal("paging backward triggered an action")
			}
			m = next.(setupModel)
		}
		if !strings.Contains(stripANSI(m.View()), "Error begins.") || m.err != wantErr || m.note != wantNote {
			t.Fatal("paging backward lost the diagnostic beginning")
		}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
		m = next.(setupModel)
		if m.err != "" || m.note != "" || m.feedbackTop != 0 || !strings.HasSuffix(m.folderIn.fields[0].value, "x") {
			t.Fatal("an ordinary editing key did not clear feedback and resume typing")
		}
	}
}

func TestSetupCheckoutDiagnosticPagingDoesNotRetryOrPaste(t *testing.T) {
	m := newSetup(context.Background(), SetupDeps{
		Checkout: func(context.Context, config.Storage) (string, func() (string, error), error) {
			t.Fatal("paging restarted checkout")
			return "", nil, nil
		},
	}, config.Default(), false)
	m.w, m.h, m.step = minW, minH, stCheckout
	m.co.failed = strings.Repeat("Checkout reported a detailed problem. ", 30) + "CHECKOUT-END."
	want := m.co.failed
	for i := 0; i < 100; i++ {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		if cmd != nil {
			t.Fatal("paging triggered retry or paste")
		}
		m = next.(setupModel)
		if m.step != stCheckout || m.co.waiting || m.co.failed != want {
			t.Fatal("paging changed checkout state")
		}
		v := stripANSI(m.View())
		for _, choice := range []string{"[r] try again", "[p] paste a key instead", "[pgup pgdn] details"} {
			if !strings.Contains(v, choice) {
				t.Fatalf("paging hides %q", choice)
			}
		}
		if strings.Contains(v, "CHECKOUT-END.") {
			return
		}
	}
	t.Fatal("checkout diagnostic tail is unreachable")
}

func TestSetupFeedbackPagingResizeAndNewError(t *testing.T) {
	m := newSetup(context.Background(), SetupDeps{}, config.Default(), false)
	m.w, m.h, m.step = minW, minH, stReview
	m.err = strings.Repeat("Saving failed with a detailed problem. ", 15) + "SAVE-END."
	for i := 0; i < 100; i++ {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		if cmd != nil {
			t.Fatal("paging started saving")
		}
		m = next.(setupModel)
	}
	if !strings.Contains(stripANSI(m.View()), "SAVE-END.") {
		t.Fatal("save diagnostic tail is unreachable")
	}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {50, 20}, {minW, minH}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(setupModel)
		_, maxTop := m.feedbackWindow()
		if m.feedbackTop < 0 || m.feedbackTop > maxTop {
			t.Fatal("resize left feedback beyond its visible range")
		}
		checkFrame(t, "resized feedback", m.View(), size)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = next.(setupModel)
	if m.feedbackTop == 0 {
		t.Fatal("fixture should have scrolled before the new error")
	}
	next, _ = m.Update(finishMsg{err: errors.New("a new error begins here")})
	m = next.(setupModel)
	if m.feedbackTop != 0 || !strings.Contains(stripANSI(m.View()), "A new error begins here.") {
		t.Fatal("a new diagnostic did not start at its beginning")
	}
}

func TestSetupFeedbackPagingKeepsRecoveryPhraseCovered(t *testing.T) {
	k, err := crypto.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	m := newSetup(context.Background(), SetupDeps{}, config.Default(), false)
	m.w, m.h, m.step, m.key = minW, minH, stPhrase, k
	m.err = strings.Repeat("Keep the recovery words covered. ", 30) + "PHRASEEND."
	for i := 0; i < 100; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		m = next.(setupModel)
		if m.showWords || m.seenWords {
			t.Fatal("paging revealed or acknowledged the phrase")
		}
		v := stripANSI(m.View())
		for i, word := range strings.Fields(k.Phrase()) {
			if strings.Contains(v, fmt.Sprintf("%2d %s", i+1, word)) {
				t.Fatalf("paging exposes recovery word %d", i+1)
			}
		}
		if strings.Contains(v, "PHRASEEND.") {
			return
		}
	}
	t.Fatal("phrase diagnostic tail is unreachable")
}

func TestSetupReviewWarningRemainsVisibleAndPageable(t *testing.T) {
	for _, size := range [][2]int{{minW, minH}, {80, 24}} {
		m := newSetup(context.Background(), SetupDeps{}, config.Default(), false)
		m.w, m.h, m.step, m.newRepo = size[0], size[1], stReview, true
		m.elsewhere = "s3://backups/" + strings.Repeat("日本語👩‍💻longfolder/", 40)
		v := stripANSI(m.View())
		for _, want := range []string{"This starts a separate set of backups.", "[pgup pgdn] details", "[s] save and finalise"} {
			if !strings.Contains(v, want) {
				t.Errorf("%dx%d review hides %q", size[0], size[1], want)
			}
		}
		for i := 0; i < 100; i++ {
			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
			if cmd != nil {
				t.Fatal("paging the warning started saving")
			}
			m = next.(setupModel)
			checkFrame(t, "review warning", m.View(), size)
			if strings.Contains(stripANSI(m.View()), "uploads everything again.") {
				break
			}
			if i == 99 {
				t.Fatal("review warning tail is inaccessible")
			}
		}
	}
}

func TestSetupShortReviewWarningKeepsExistingLayout(t *testing.T) {
	m := newSetup(context.Background(), SetupDeps{}, config.Default(), false)
	m.w, m.h, m.step, m.newRepo, m.elsewhere = 80, 24, stReview, true, "s3://backups/frost/"
	p := m.page(76, 17)
	if p.feedback != "" || len(p.extra) != 1 {
		t.Fatal("a fitting warning should keep its existing position")
	}
	if strings.Contains(stripANSI(lastLine(m.View())), "[pgup pgdn] details") {
		t.Fatal("a fitting warning should not add a paging hint")
	}
}

func TestSetupLongSelectedMissingFolderKeepsWarning(t *testing.T) {
	m := newSetup(context.Background(), SetupDeps{DirExists: func(string) bool { return false }}, config.Default(), false)
	m.cfg.Paths = []string{"/" + strings.Repeat("日本語👩‍💻longfolder/", 40)}
	m.folderSel = 0
	p := m.foldersPage(52, 1)
	if !strings.Contains(stripANSI(p.body[0]), "not found") {
		t.Fatal("selecting a long missing folder hides its warning")
	}
	if lipgloss.Width(p.body[0]) != 52 {
		t.Fatal("the missing-folder warning exceeds its row")
	}
}

func TestSetupUnicodeDiagnosticsAcrossStagesAndResize(t *testing.T) {
	shots, _ := walkNewSetup(t, 80, 24)
	for name, m := range otherScreens(t, 80, 24) {
		shots[name] = m
	}
	stages := make(map[setupStep]setupModel)
	for _, shot := range shots {
		m := shot.(setupModel)
		if !m.quit && m.busy == "" {
			stages[m.step] = m
		}
	}
	for st, original := range stages {
		t.Run(fmt.Sprintf("step-%d", st), func(t *testing.T) {
			m := original
			m.err = strings.Repeat("日本語👩‍💻unbroken", 40) + " ENDCHECK."
			for _, size := range [][2]int{{minW, minH}, {120, 40}, {50, 20}, {1, 1}, {80, 24}} {
				next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				m = next.(setupModel)
				checkFrame(t, "Unicode resized setup", m.View(), size)
				if size[0] < minW || size[1] < minH || st == stWelcome || st == stDone {
					continue
				}
				rows, maxTop := m.feedbackWindow()
				if rows < 1 {
					t.Fatal("wizard stage has no rows for its diagnostic")
				}
				for m.feedbackTop < maxTop {
					next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
					if cmd != nil {
						t.Fatal("paging across wizard stages triggered an action")
					}
					m = next.(setupModel)
				}
				if !strings.Contains(stripANSI(m.View()), "ENDCHECK.") {
					t.Fatal("Unicode diagnostic tail is inaccessible")
				}
			}
		})
	}
}
