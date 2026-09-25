package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/rhymeswithlimo/frost/internal/sound"
)

func newTestArcade(t *testing.T) *arcade {
	t.Helper()
	a := newArcade(filepath.Join(t.TempDir(), "best.json"), 42)
	a.resize(40, 20)
	a.start()
	return a
}

// step runs one tick with spawning switched off by clearing new things.
func (a *arcade) stepNoSpawn() {
	before := len(a.things)
	a.tick(arcadeTickMsg{a.gen})
	a.things = a.things[:min(before, len(a.things))]
}

func TestShootThawCatch(t *testing.T) {
	a := newTestArcade(t)
	a.things = []thing{{kind: kindFrozen, ext: "pdf", x: a.shipX - 1, y: 5, vy: 0, hp: 2}}

	// Two hits crack both layers and free the file.
	for shots := 0; shots < 2; shots++ {
		a.cooldown = 0
		a.key(" ")
		for i := 0; i < 20 && len(a.bullets) > 0; i++ {
			a.stepNoSpawn()
		}
	}
	if len(a.things) != 1 || a.things[0].kind != kindThawed {
		t.Fatalf("file not thawed: %+v", a.things)
	}
	if a.score != 2*10+25 {
		t.Fatalf("score after thawing = %d", a.score)
	}

	// The free file falls onto the ship and is rescued.
	for i := 0; i < 60 && len(a.things) > 0; i++ {
		a.stepNoSpawn()
	}
	if a.rescued != 1 || a.combo != 1 || a.score != 45+50 {
		t.Fatalf("rescued=%d combo=%d score=%d", a.rescued, a.combo, a.score)
	}
}

func TestBulletsCantHurtRot(t *testing.T) {
	a := newTestArcade(t)
	a.things = []thing{{kind: kindRot, x: a.shipX, y: 3, vy: 0}}
	a.key(" ")
	for i := 0; i < 20; i++ {
		a.stepNoSpawn()
	}
	if len(a.things) != 1 || a.score != 0 {
		t.Fatalf("rot was hurt: things=%+v score=%d", a.things, a.score)
	}
}

func TestRotCostsALife(t *testing.T) {
	a := newTestArcade(t)
	a.things = []thing{{kind: kindRot, x: a.shipX, y: float64(a.h - 2), vy: 1}}
	a.stepNoSpawn()
	if a.lives != startLives-1 || a.invuln == 0 {
		t.Fatalf("lives=%d invuln=%d", a.lives, a.invuln)
	}
}

func TestFrozenFileAtBottomCostsALife(t *testing.T) {
	a := newTestArcade(t)
	a.shipX = 0
	a.things = []thing{{kind: kindFrozen, ext: "zip", hp: 1, x: 30, y: float64(a.h - 1), vy: 1}}
	a.stepNoSpawn()
	if a.lives != startLives-1 {
		t.Fatalf("lives = %d", a.lives)
	}
}

func TestGameOverSavesBest(t *testing.T) {
	a := newTestArcade(t)
	a.score, a.lives = 1234, 1
	a.things = []thing{{kind: kindRot, x: a.shipX, y: float64(a.h - 2), vy: 1}}
	if cmd := a.tick(arcadeTickMsg{a.gen}); cmd != nil {
		t.Fatal("tick loop kept running after game over")
	}
	if a.phase != phaseOver || !a.newBest || a.best != 1234 {
		t.Fatalf("phase=%v newBest=%v best=%d", a.phase, a.newBest, a.best)
	}
	if got := loadBest(a.bestPath); got != 1234 {
		t.Fatalf("saved best = %d", got)
	}
	if v := a.view(); !strings.Contains(v, "NEW PERSONAL BEST!") || !strings.Contains(v, "1234") {
		t.Fatal("game over screen missing score or new best")
	}

	// A lower score doesn't replace it.
	b := newArcade(a.bestPath, 1)
	b.resize(40, 20)
	b.start()
	b.score = 10
	b.gameOver()
	if b.newBest || loadBest(a.bestPath) != 1234 {
		t.Fatal("lower score replaced the best")
	}

	// Enter starts a fresh game.
	if cmd, done := a.key("enter"); cmd == nil || done || a.phase != phasePlaying || a.score != 0 || a.lives != startLives {
		t.Fatal("enter didn't restart")
	}
}

func TestStaleTicksIgnored(t *testing.T) {
	a := newTestArcade(t)
	old := a.gen
	a.key("p")
	a.key("p") // resuming starts a new tick loop
	if cmd := a.tick(arcadeTickMsg{old}); cmd != nil {
		t.Fatal("stale tick loop kept running")
	}
}

func TestEscLeaves(t *testing.T) {
	a := newTestArcade(t)
	a.score = 500
	if _, done := a.key("esc"); !done {
		t.Fatal("esc didn't leave")
	}
	if loadBest(a.bestPath) != 500 {
		t.Fatal("leaving mid-game didn't keep the best score")
	}
}

func TestArcadeRendersToSize(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	for _, size := range [][2]int{{64, 24}, {40, 14}, {20, 8}} {
		a := newArcade("", 7)
		a.resize(size[0], size[1])
		for _, phase := range []string{"title", "playing", "over"} {
			switch phase {
			case "playing":
				a.start()
				for range 200 {
					a.tick(arcadeTickMsg{a.gen})
					a.key("right")
					a.key(" ")
				}
			case "over":
				a.gameOver()
			}
			v := a.view()
			if dir := os.Getenv("FROST_TUI_DUMP"); dir != "" {
				os.WriteFile(filepath.Join(dir, fmt.Sprintf("game-%s-%d.ans", phase, size[0])), []byte(v), 0o644)
			}
			if a.tooSmall() {
				if !strings.Contains(v, "bigger") {
					t.Errorf("%v: small window message missing", size)
				}
				continue
			}
			if phase == "title" && !strings.Contains(v, "press [space] to start") {
				t.Errorf("%v: title screen lost the start prompt", size)
			}
			if w := lipgloss.Width(v); w != a.w+2 {
				t.Errorf("%v %s: width %d, want %d", size, phase, w, a.w+2)
			}
			if h := lipgloss.Height(v); h != a.h+3 {
				t.Errorf("%v %s: height %d, want %d", size, phase, h, a.h+3)
			}
		}
	}
}

func TestSpreadWidensWithLevel(t *testing.T) {
	a := newTestArcade(t)
	a.resize(64, 24)
	prev := 0
	for lvl := 1; lvl <= 6; lvl++ {
		a.level, a.diff = lvl, float64(lvl)
		s := a.spread()
		if s < prev {
			t.Fatalf("spread shrank at level %d: %d < %d", lvl, s, prev)
		}
		prev = s
	}
	a.level, a.diff = 1, 1
	if s := a.spread(); s > 20 {
		t.Fatalf("level 1 spread = %d, want files clustered", s)
	}
	a.level, a.diff = 5, 5
	if s := a.spread(); s != a.w {
		t.Fatalf("level 5 spread = %d, want the full width %d", s, a.w)
	}
}

func TestEarlyFilesCluster(t *testing.T) {
	a := newTestArcade(t)
	a.resize(64, 24)
	a.start()
	for range 3000 {
		a.things = nil // keep the top clear so every roll can spawn
		a.spawn()
		for _, th := range a.things {
			if th.kind != kindFrozen {
				continue
			}
			if th.x < 0 || th.x+th.width() > a.w {
				t.Fatalf("file off the field: %+v", th)
			}
		}
	}
	// Every level 1 file lands within the level 1 window of the last one.
	a.lastX = 32
	for range 500 {
		a.things = nil
		before := a.lastX
		a.spawn()
		for _, th := range a.things {
			if th.kind == kindFrozen && abs(th.x-before) > a.spread() {
				t.Fatalf("level 1 file spawned %d columns from the last one", abs(th.x-before))
			}
		}
	}
}

func TestRotRidesWithFiles(t *testing.T) {
	a := newTestArcade(t)
	a.resize(64, 24)
	a.level, a.diff = 4, 4
	f := thing{kind: kindFrozen, ext: "pdf", x: 30, vy: 0.1}
	seen := 0
	for range 200 {
		a.things = nil
		a.spawnCluster(f)
		for _, r := range a.things {
			seen++
			if r.kind != kindRot || r.vy < f.vy*0.9 || r.vy > f.vy*1.1 {
				t.Fatalf("shard doesn't travel with its file: %+v", r)
			}
			if r.x < f.x-5 || r.x > f.x+f.width()+3 {
				t.Fatalf("shard too far from its file: x=%d file x=%d", r.x, f.x)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no bit rot spawned at level 4")
	}

	// Level 1 is gentle: at most one shard per file.
	a.level, a.diff = 1, 1
	for range 200 {
		a.things = nil
		a.spawnCluster(f)
		if len(a.things) > 1 {
			t.Fatalf("level 1 spawned %d shards", len(a.things))
		}
	}
}

func abs(n int) int { return max(n, -n) }

func TestDifficultyRampsSlowly(t *testing.T) {
	a := newTestArcade(t)
	a.things = nil
	for range 20 * 60 { // one minute
		a.tick(arcadeTickMsg{a.gen})
		a.things, a.lives = nil, startLives
	}
	if a.diff < 1.3 || a.diff > 1.5 || a.level != 1 {
		t.Fatalf("after 1 minute diff=%.2f level=%d", a.diff, a.level)
	}
	for range 20 * 3 * 60 { // four minutes in
		a.tick(arcadeTickMsg{a.gen})
		a.things, a.lives = nil, startLives
	}
	if a.diff < 2.4 || a.diff > 2.8 {
		t.Fatalf("after 4 minutes diff=%.2f", a.diff)
	}
}

func TestPowerUps(t *testing.T) {
	a := newTestArcade(t)
	rescue := func() {
		a.things = []thing{{kind: kindThawed, ext: "pdf", x: a.shipX, y: float64(a.h - 1), vy: 0}}
		a.stepNoSpawn()
	}
	for range rescuesPerUp - 1 {
		rescue()
	}
	if a.power != 0 {
		t.Fatalf("power %d after %d rescues", a.power, rescuesPerUp-1)
	}
	rescue()
	if a.power != 1 || a.charge != 0 {
		t.Fatalf("power=%d charge=%d after %d rescues", a.power, a.charge, rescuesPerUp)
	}

	// Power 1 fires faster.
	a.bullets, a.cooldown = nil, 0
	a.fire()
	if a.cooldown != fireCooldown-1 {
		t.Fatalf("cooldown at power 1 = %d", a.cooldown)
	}

	// Power 3 fires twin shots.
	a.power, a.bullets, a.cooldown = 3, nil, 0
	a.fire()
	if len(a.bullets) != 2 || a.bullets[0].x != a.shipX || a.bullets[1].x != a.shipX+2 {
		t.Fatalf("twin shot = %+v", a.bullets)
	}

	// Losing a life costs a power level.
	a.invuln = 0
	a.loseLife()
	if a.power != 2 || a.charge != 0 {
		t.Fatalf("after losing a life power=%d charge=%d", a.power, a.charge)
	}
}

func TestThreatGrows(t *testing.T) {
	a := newTestArcade(t)
	a.diff = 1
	start := a.threat()
	a.diff = 3
	late := a.threat()
	a.power = 3
	if !(start < late && late < a.threat()) {
		t.Fatal("threat should grow with time and with power")
	}
}

func TestSoundAssets(t *testing.T) {
	for _, name := range []string{"laser-shoot.wav", "pickup-file.wav", "hit-hurt.wav", "explosion.wav", "power-up.wav"} {
		b, err := sfxFiles.ReadFile("assets/sfx/" + name)
		if err != nil {
			t.Fatalf("%s isn't embedded: %v", name, err)
		}
		if err := sound.Validate(b); err != nil {
			t.Errorf("%s can't be played: %v", name, err)
		}
	}
}

func TestMuteIsRemembered(t *testing.T) {
	a := newTestArcade(t)
	if a.muted {
		t.Fatal("sound should start on")
	}
	a.key("m")
	if !a.muted {
		t.Fatal("[m] didn't mute")
	}
	if b := newArcade(a.bestPath, 1); !b.muted {
		t.Fatal("mute wasn't remembered")
	}
	a.key("m")
	if b := newArcade(a.bestPath, 1); b.muted {
		t.Fatal("unmute wasn't remembered")
	}
}

func TestTitleShowsWordmark(t *testing.T) {
	a := newArcade("", 1)
	a.resize(64, 24)
	v := a.view()
	first := strings.Split(strings.TrimRight(gameWordmark, "\n "), "\n")[1]
	if !strings.Contains(v, strings.TrimSpace(first)) {
		t.Fatal("title screen doesn't show the icebreaker wordmark")
	}
	small := newArcade("", 1)
	small.resize(34, 14)
	if !strings.Contains(small.view(), "I C E B R E A K E R") {
		t.Fatal("narrow title should fall back to plain text")
	}
}

func TestSoundsFireAtTheRightMoments(t *testing.T) {
	a := newTestArcade(t)
	var got []string
	a.heard = func(name string) { got = append(got, name) }
	last := func() string {
		if len(got) == 0 {
			return ""
		}
		return got[len(got)-1]
	}

	// Two layers of ice: the first hit cracks it, the second breaks it.
	a.things = []thing{{kind: kindFrozen, ext: "pdf", x: a.shipX - 1, y: 5, hp: 2}}
	for _, want := range []string{"crack", "explosion"} {
		a.cooldown = 0
		a.key(" ")
		if last() != "shoot" {
			t.Fatalf("firing played %q", last())
		}
		for i := 0; i < 20 && len(a.bullets) > 0; i++ {
			a.stepNoSpawn()
		}
		if last() != want {
			t.Fatalf("hit played %q, want %q", last(), want)
		}
	}

	// A bullet bouncing off bit rot dings.
	a.things = []thing{{kind: kindRot, x: a.shipX, y: 5}}
	a.cooldown = 0
	a.key(" ")
	for i := 0; i < 20 && len(a.bullets) > 0; i++ {
		a.stepNoSpawn()
	}
	if last() != "ding" {
		t.Fatalf("hitting bit rot played %q", last())
	}
	a.things = nil

	// A normal catch plays the pickup, the catch that powers up plays
	// the power-up sound instead.
	for i := range rescuesPerUp {
		a.things = []thing{{kind: kindThawed, ext: "pdf", x: a.shipX, y: float64(a.h - 1)}}
		a.stepNoSpawn()
		want := "pickup"
		if i == rescuesPerUp-1 {
			want = "powerup"
		}
		if last() != want {
			t.Fatalf("catch %d played %q, want %q", i+1, last(), want)
		}
	}

	// Muted: nothing plays.
	a.key("m")
	n := len(got)
	a.cooldown = 0
	a.key(" ")
	if len(got) != n {
		t.Fatal("a sound played while muted")
	}
}
