package tui

// Icebreaker: a small arcade game hidden behind [i].
//
// Frozen files drift down the screen. Shoot them to crack the ice, then catch
// the thawed file with your ship to rescue it. Bit rot travels with the files
// and can't be shot. A frozen file reaching the bottom or a hit from bit rot
// costs a life, and three lost lives end the game.
//
// Two things scale. Threat grows smoothly with time: files arrive more often,
// fall faster, wear thicker ice and spread further apart, and more bit rot
// rides with them. Power grows with skill: every 8 rescues upgrades the ship
// (faster shots, more shots, twin shots), each power level nudges the threat
// up a little, and losing a life costs a power level.

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rhymeswithlimo/frost/internal/sound"
	"github.com/rhymeswithlimo/frost/internal/theme"
)

const (
	arcadeTick   = 50 * time.Millisecond
	arcadeMaxW   = 64
	arcadeMaxH   = 24
	arcadeMinW   = 30
	arcadeMinH   = 12
	startLives   = 3
	fireCooldown = 3    // ticks between shots, one less from power 1
	maxBullets   = 4    // on screen at once, two more from power 2
	rescuesPerUp = 8    // rescues needed for the next power level
	maxPower     = 3    // power 3 adds twin shots
	invulnTicks  = 27   // after losing a life
	levelTicks   = 3000 // about 2.5 minutes per level
)

type arcadePhase int

const (
	phaseTitle arcadePhase = iota
	phasePlaying
	phasePaused
	phaseOver
)

type thingKind int

const (
	kindFrozen thingKind = iota // an iced file: shoot it
	kindThawed                  // a free file: catch it
	kindRot                     // bit rot: dodge it
)

// thing is anything that falls.
type thing struct {
	kind thingKind
	x    int // left column
	y    float64
	vy   float64
	hp   int    // ice layers left, for frozen files
	ext  string // "pdf", "jpg", ...
}

func (t thing) width() int {
	switch t.kind {
	case kindFrozen:
		return len(t.ext) + 2
	case kindThawed:
		return len(t.ext)
	}
	return 2
}

func (t thing) row() int { return int(t.y) }

type bullet struct{ x, y int }

// spark is a short-lived effect: ice shards or a score popup.
type spark struct {
	x, y int
	text string
	ttl  int
	st   cellStyle
	hang bool // text popups hang in place, ice shards fall
}

var fileExts = []string{"pdf", "jpg", "doc", "mp3", "zip", "txt", "png", "mov", "csv", "key", "psd", "wav"}

type arcadeTickMsg struct{ gen int }

type arcade struct {
	w, h  int // playfield size in cells
	rng   *rand.Rand
	phase arcadePhase
	gen   int // tick generation, so a stale tick loop dies quietly

	shipX    int // left column of the 3-wide ship
	lastX    int // where the last file spawned, files cluster around it early on
	bullets  []bullet
	things   []thing
	sparks   []spark
	cooldown int
	invuln   int

	ticks    int
	score    int
	lives    int
	combo    int
	rescued  int
	level    int
	power    int     // ship upgrades earned by rescuing files
	charge   int     // rescues towards the next power level
	diff     float64 // difficulty: 1 at the start, +1 per level, rising smoothly
	best     int
	newBest  bool
	bestPath string

	snd   *sound.Player // nil means silent, as in tests
	muted bool
	heard func(string) // tests use this to see which sounds play
}

func newArcade(bestPath string, seed uint64) *arcade {
	a := &arcade{rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), bestPath: bestPath}
	saved := loadSaved(bestPath)
	a.best, a.muted = saved.Best, saved.Muted
	return a
}

// play starts a sound effect unless the player muted the game.
func (a *arcade) play(name string) {
	if a.muted {
		return
	}
	if a.heard != nil {
		a.heard(name)
	}
	a.snd.Play(name)
}

// ---- persistence ----

// saved is what the game remembers between runs.
type saved struct {
	Best  int  `json:"best"`
	Muted bool `json:"muted,omitempty"`
}

func loadSaved(path string) saved {
	var sv saved
	if raw, err := os.ReadFile(path); err == nil {
		json.Unmarshal(raw, &sv)
	}
	return sv
}

func loadBest(path string) int { return loadSaved(path).Best }

func (a *arcade) save() {
	if a.bestPath == "" {
		return
	}
	os.MkdirAll(filepath.Dir(a.bestPath), 0o700)
	raw, _ := json.Marshal(saved{Best: a.best, Muted: a.muted})
	os.WriteFile(a.bestPath, raw, 0o600)
}

// ---- lifecycle ----

// resize fits the playfield to the space available.
func (a *arcade) resize(w, h int) {
	a.w, a.h = min(w, arcadeMaxW), min(h, arcadeMaxH)
	a.shipX = max(0, min(a.shipX, a.w-3))
}

func (a *arcade) tooSmall() bool { return a.w < arcadeMinW || a.h < arcadeMinH }

func (a *arcade) start() tea.Cmd {
	a.phase = phasePlaying
	a.bullets, a.things, a.sparks = nil, nil, nil
	a.ticks, a.score, a.combo, a.rescued, a.cooldown, a.invuln = 0, 0, 0, 0, 0, 0
	a.power, a.charge = 0, 0
	a.lives, a.level, a.diff, a.newBest = startLives, 1, 1, false
	a.shipX = a.w/2 - 1
	a.lastX = a.w / 2
	return a.tickCmd()
}

func (a *arcade) tickCmd() tea.Cmd {
	a.gen++
	gen := a.gen
	return tea.Tick(arcadeTick, func(time.Time) tea.Msg { return arcadeTickMsg{gen} })
}

// key handles a key press. It returns done=true when the player leaves.
func (a *arcade) key(k string) (cmd tea.Cmd, done bool) {
	if k == "m" { // mute works on every screen of the game
		a.muted = !a.muted
		a.save()
		return nil, false
	}
	if k == "esc" {
		if a.phase == phasePlaying || a.phase == phasePaused {
			a.gameOver() // leaving counts as a finished game for the best score
		}
		return nil, true
	}
	switch a.phase {
	case phaseTitle:
		if k == " " || k == "enter" {
			return a.start(), false
		}
	case phaseOver:
		if k == "enter" || k == " " {
			return a.start(), false
		}
	case phasePaused:
		if k == "p" || k == " " {
			a.phase = phasePlaying
			return a.tickCmd(), false
		}
	case phasePlaying:
		switch k {
		case "left", "a", "h":
			a.shipX = max(0, a.shipX-2)
		case "right", "d", "l":
			a.shipX = min(a.w-3, a.shipX+2)
		case " ", "up", "w", "k":
			a.fire()
		case "p":
			a.phase = phasePaused
		}
	}
	return nil, false
}

func (a *arcade) fire() {
	limit, cooldown := maxBullets, fireCooldown
	if a.power >= 1 {
		cooldown--
	}
	if a.power >= 2 {
		limit += 2
	}
	if a.cooldown > 0 || len(a.bullets) >= limit {
		return
	}
	if a.power >= 3 { // twin shot from the wings
		a.bullets = append(a.bullets, bullet{x: a.shipX, y: a.h - 2}, bullet{x: a.shipX + 2, y: a.h - 2})
	} else {
		a.bullets = append(a.bullets, bullet{x: a.shipX + 1, y: a.h - 2})
	}
	a.cooldown = cooldown
	a.play("shoot")
}

// threat is how hard the game pushes: 0 at the start, +1 per level of
// time, plus a little for every power level so a strong ship stays
// challenged.
func (a *arcade) threat() float64 {
	return a.diff - 1 + 0.15*float64(a.power)
}

// tick advances the game one step. It returns the next tick, or nil when
// the game stopped.
func (a *arcade) tick(msg arcadeTickMsg) tea.Cmd {
	if msg.gen != a.gen || a.phase != phasePlaying {
		return nil
	}
	a.ticks++
	// Difficulty rises a little every tick instead of jumping at each level,
	// so the game gets harder slowly and without sudden walls.
	a.diff = 1 + float64(a.ticks)/levelTicks
	a.level = int(a.diff)
	if a.cooldown > 0 {
		a.cooldown--
	}
	if a.invuln > 0 {
		a.invuln--
	}

	a.spawn()

	// Bullets move first and are checked, then things move and are checked
	// again, so nothing can pass through a bullet between frames.
	for i := range a.bullets {
		a.bullets[i].y--
	}
	a.hitBullets()
	for i := range a.things {
		a.things[i].y += a.things[i].vy
	}
	a.hitBullets()
	a.bottom()

	var live []spark
	for _, s := range a.sparks {
		if s.ttl--; s.ttl > 0 {
			if !s.hang {
				s.y++
			}
			live = append(live, s)
		}
	}
	a.sparks = live

	if a.lives <= 0 {
		a.gameOver()
		return nil
	}
	return a.tickCmd()
}

func (a *arcade) spawn() {
	lvl := 1 + a.threat() // 1 at the start, like a.diff, plus the power bump
	if a.rng.Float64() >= 0.024+0.0065*lvl {
		return
	}
	f := thing{kind: kindFrozen, ext: fileExts[a.rng.IntN(len(fileExts))], vy: 0.048 + 0.011*lvl + a.rng.Float64()*0.028}
	f.hp = 1 + a.rng.IntN(min(3, 1+int(lvl)/2))

	// Early on, files land near the last one so they're easy to reach. The
	// window widens each level until they can spawn anywhere.
	span := a.spread()
	lo := max(0, min(a.lastX-span/2, a.w-f.width()-span))
	f.x = min(lo+a.rng.IntN(max(1, span)), a.w-f.width())
	if a.crowded(f) {
		return
	}
	a.lastX = f.x + f.width()/2
	a.things = append(a.things, f)
	a.spawnCluster(f)
}

// spread is how many columns wide the spawn window is: about 16 at the
// start, widening smoothly to the whole field by level 5.
func (a *arcade) spread() int {
	full := a.w
	return min(full, 16+int((a.diff-1)*float64(full-16)/4))
}

// spawnCluster surrounds a new file with bit rot: beside it, or under it
// where it blocks some of the shots. Each shard sits a random cell or two
// out and falls at nearly the file's speed, so a cluster arrives together
// but loosens a little on the way down. Shards grow with difficulty.
func (a *arcade) spawnCluster(f thing) {
	lvl := 1 + a.threat()
	shards := 0
	for range min(3, int(lvl)) {
		if a.rng.Float64() < 0.3+0.09*lvl {
			shards++
		}
	}
	for _, spot := range a.rng.Perm(3)[:shards] {
		gap := 1 + a.rng.IntN(3)           // 1 to 3 cells from the file
		drift := 0.9 + a.rng.Float64()*0.2 // within 10% of the file's speed
		r := thing{kind: kindRot, vy: f.vy * drift, y: f.y}
		switch spot {
		case 0: // left
			r.x = f.x - r.width() - gap
		case 1: // right
			r.x = f.x + f.width() + gap
		case 2: // underneath, covering part of the file
			r.x = f.x + a.rng.IntN(max(1, f.width()-r.width()+1))
			r.y = f.y + 1 + float64(gap)*0.75
		}
		if r.x >= 0 && r.x+r.width() <= a.w {
			a.things = append(a.things, r)
		}
	}
}

// crowded reports whether t would overlap something near the top.
func (a *arcade) crowded(t thing) bool {
	for _, o := range a.things {
		if o.y < 2 && t.x < o.x+o.width()+1 && o.x < t.x+t.width()+1 {
			return true
		}
	}
	return false
}

func (a *arcade) hitBullets() {
	var keep []bullet
	for _, b := range a.bullets {
		if b.y < 0 {
			continue
		}
		hit := false
		for i := range a.things {
			t := &a.things[i]
			if t.row() != b.y || b.x < t.x || b.x >= t.x+t.width() {
				continue
			}
			switch t.kind {
			case kindThawed:
				continue // bullets pass over free files
			case kindRot:
				a.play("ding") // bullets bounce off bit rot
				a.sparks = append(a.sparks, spark{x: b.x, y: b.y, text: "x", ttl: 3, st: stSpark})
			case kindFrozen:
				t.hp--
				a.score += 10
				a.shatter(t.x, t.row(), t.width())
				if t.hp > 0 {
					a.play("crack") // a layer cracks, more to go
				} else {
					a.play("explosion") // the last of the ice breaks off
					t.kind = kindThawed
					t.x++ // the ice was one cell either side
					t.vy = 0.45
					a.score += 25
				}
			}
			hit = true
			break
		}
		if !hit {
			keep = append(keep, b)
		}
	}
	a.bullets = keep
}

func (a *arcade) shatter(x, y, w int) {
	for range 3 {
		a.sparks = append(a.sparks, spark{
			x: x + a.rng.IntN(max(w, 1)), y: y, ttl: 3 + a.rng.IntN(3),
			text: string("*'.,"[a.rng.IntN(4)]), st: stSpark,
		})
	}
}

// bottom handles things reaching the ship's row.
func (a *arcade) bottom() {
	shipRow := a.h - 1
	var keep []thing
	for _, t := range a.things {
		overShip := t.x < a.shipX+3 && a.shipX < t.x+t.width()
		switch {
		case t.row() >= shipRow && t.kind == kindThawed && overShip:
			a.combo++
			gain := 50 * a.combo
			a.score += gain
			a.rescued++
			powered := false
			a.sparks = append(a.sparks, spark{x: max(0, a.shipX-1), y: shipRow - 2, text: fmt.Sprintf("+%d", gain), ttl: 12, st: stGood, hang: true})
			if a.power < maxPower {
				if a.charge++; a.charge >= rescuesPerUp {
					a.power, a.charge = a.power+1, 0
					powered = true
					a.sparks = append(a.sparks, spark{x: max(0, a.w/2-4), y: a.h / 2, text: "POWER UP", ttl: 24, st: stGood, hang: true})
				}
			}
			if powered {
				a.play("powerup") // replaces the pickup sound for this catch
			} else {
				a.play("pickup")
			}
			continue
		case t.row() >= shipRow && t.kind == kindRot && overShip:
			a.loseLife()
			continue
		case t.row() >= shipRow+1 && t.kind == kindFrozen:
			a.loseLife() // a file froze solid
			a.sparks = append(a.sparks, spark{x: t.x, y: shipRow - 1, text: "lost ." + t.ext, ttl: 14, st: stGood, hang: true})
			continue
		case t.row() >= shipRow+1:
			if t.kind == kindThawed {
				a.combo = 0 // dropped a free file
				a.sparks = append(a.sparks, spark{x: t.x, y: shipRow - 1, text: "missed ." + t.ext, ttl: 12, st: stIce2, hang: true})
			}
			continue
		}
		keep = append(keep, t)
	}
	a.things = keep
}

func (a *arcade) loseLife() {
	if a.invuln > 0 {
		return
	}
	a.lives--
	a.play("hurt")
	a.combo = 0
	a.power, a.charge = max(0, a.power-1), 0
	a.invuln = invulnTicks
	a.shatter(a.shipX, a.h-1, 3)
}

func (a *arcade) gameOver() {
	if a.phase == phaseOver {
		return
	}
	a.phase = phaseOver
	a.gen++ // stop the tick loop
	if a.score > a.best {
		a.best, a.newBest = a.score, true
		a.save()
	}
}

// ---- drawing ----

type cellStyle int

const (
	stBlank cellStyle = iota
	stShip
	stBullet
	stIce3
	stIce2
	stIce1
	stFile
	stThawed
	stRot
	stSpark
	stGood
)

var arcadeStyles = map[cellStyle]lipgloss.Style{
	stBlank:  theme.Base,
	stShip:   theme.Bold,
	stBullet: theme.Bold,
	stIce3:   theme.Text,
	stIce2:   theme.Dim,
	stIce1:   theme.Faded,
	stFile:   theme.Bold,
	stThawed: theme.Good.Bold(true),
	stRot:    theme.Error,
	stSpark:  theme.Dim,
	stGood:   theme.Caution.Bold(true),
}

type canvas struct {
	w, h  int
	r     [][]rune
	style [][]cellStyle
}

func newCanvas(w, h int) *canvas {
	c := &canvas{w: w, h: h, r: make([][]rune, h), style: make([][]cellStyle, h)}
	for y := range h {
		c.r[y] = []rune(strings.Repeat(" ", w))
		c.style[y] = make([]cellStyle, w)
	}
	return c
}

func (c *canvas) put(x, y int, s string, st cellStyle) {
	if y < 0 || y >= c.h {
		return
	}
	for i, r := range []rune(s) {
		if xx := x + i; xx >= 0 && xx < c.w {
			c.r[y][xx], c.style[y][xx] = r, st
		}
	}
}

func (c *canvas) center(y int, s string, st cellStyle) {
	c.put((c.w-len([]rune(s)))/2, y, s, st)
}

func (c *canvas) render() string {
	lines := make([]string, c.h)
	for y := range c.h {
		var b strings.Builder
		start := 0
		for x := 1; x <= c.w; x++ {
			if x == c.w || c.style[y][x] != c.style[y][start] {
				b.WriteString(arcadeStyles[c.style[y][start]].Render(string(c.r[y][start:x])))
				start = x
			}
		}
		lines[y] = b.String()
	}
	return strings.Join(lines, "\n")
}

func (a *arcade) hud() string {
	if a.phase == phaseTitle {
		return theme.Dim.Render("BEST ") + theme.Text.Render(fmt.Sprintf("%06d", a.best))
	}
	lives := strings.Repeat("A ", max(a.lives, 0))
	// Most important first: joinFit drops from the end when space is short.
	parts := []string{
		theme.Dim.Render("SCORE ") + theme.Bold.Render(fmt.Sprintf("%06d", a.score)),
		theme.Dim.Render("LIVES ") + theme.Bold.Render(strings.TrimSpace(lives)),
		theme.Dim.Render("PWR ") + theme.Bold.Render(strings.Repeat("■", a.power)) + theme.Faded.Render(strings.Repeat("□", maxPower-a.power)),
	}
	if a.combo > 1 {
		parts = append(parts, theme.Caution.Bold(true).Render(fmt.Sprintf("COMBO x%d", a.combo)))
	}
	parts = append(parts,
		theme.Dim.Render("LVL ")+theme.Text.Render(fmt.Sprint(a.level)),
		theme.Dim.Render("BEST ")+theme.Text.Render(fmt.Sprintf("%06d", max(a.best, a.score))),
	)
	return joinFit(parts, a.w)
}

func (a *arcade) view() string {
	if a.tooSmall() {
		return theme.Text.Render(fmt.Sprintf("Make the window bigger to play (at least %dx%d).", arcadeMinW+4, arcadeMinH+6))
	}
	c := newCanvas(a.w, a.h)

	switch a.phase {
	case phaseTitle:
		a.drawTitle(c)
	case phaseOver:
		a.drawOver(c)
	default:
		a.drawField(c)
		if a.phase == phasePaused {
			c.center(a.h/2-1, "  PAUSED  ", stFile)
			c.center(a.h/2+1, "[p] resume   [esc] leave", stIce2)
		}
	}

	box := theme.Box(true).Padding(0, 0).Render(c.render())
	return stack(pad(a.hud(), lipgloss.Width(box)), box)
}

func (a *arcade) drawField(c *canvas) {
	for _, t := range a.things {
		y := t.row()
		switch t.kind {
		case kindFrozen:
			ice := map[int]string{3: "▓", 2: "▒", 1: "░"}[t.hp]
			st := map[int]cellStyle{3: stIce3, 2: stIce2, 1: stIce1}[t.hp]
			c.put(t.x, y, ice, st)
			c.put(t.x+1, y, t.ext, stIce2)
			c.put(t.x+1+len(t.ext), y, ice, st)
		case kindThawed:
			c.put(t.x, y, t.ext, stThawed)
		case kindRot:
			c.put(t.x, y, "▚▞", stRot)
		}
	}
	for _, b := range a.bullets {
		c.put(b.x, b.y, "|", stBullet)
	}
	for _, s := range a.sparks {
		c.put(s.x, s.y, s.text, s.st)
	}
	if a.invuln == 0 || a.invuln%4 < 2 { // blink after a hit
		c.put(a.shipX, a.h-1, "/A\\", stShip)
	}
}

func (a *arcade) drawTitle(c *canvas) {
	mark := strings.Split(strings.TrimRight(gameWordmark, "\n "), "\n")
	markW := 0
	for _, l := range mark {
		markW = max(markW, len([]rune(l)))
	}
	if markW+4 > c.w || len(mark)+8 > c.h {
		mark = []string{"I C E B R E A K E R"} // no room for the wordmark
	}
	desc := wrapWords("Shoot the ice, catch your files, dodge the bit rot.", c.w-6)

	y := max(1, (c.h-len(mark)-len(desc)-6)/2)
	left := (c.w - markW) / 2
	for _, l := range mark {
		if len(mark) == 1 {
			c.center(y, l, stFile)
		} else {
			c.put(left, y, l, stFile) // keep the art's own alignment
		}
		y++
	}
	y++
	for _, l := range desc {
		c.center(y, l, stIce2)
		y++
	}
	c.center(y+1, "press [space] to start", stFile)
	if a.best > 0 {
		c.center(y+3, fmt.Sprintf("personal best %d", a.best), stIce1)
	}
}

// wrapWords breaks s into lines of at most w runes.
func wrapWords(s string, w int) []string {
	if s == "" {
		return []string{""}
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case len([]rune(line))+1+len([]rune(word)) <= w:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	return append(lines, line)
}

func (a *arcade) drawOver(c *canvas) {
	y := max(1, a.h/2-5)
	c.center(y, "G A M E   O V E R", stRot)
	c.center(y+2, fmt.Sprintf("score  %d", a.score), stFile)
	if a.newBest {
		c.center(y+3, "NEW PERSONAL BEST!", stGood)
	} else {
		c.center(y+3, fmt.Sprintf("personal best  %d", a.best), stIce3)
	}
	c.center(y+5, fmt.Sprintf("files rescued %d   level %d", a.rescued, a.level), stIce2)
	c.center(y+7, "[enter] play again   [esc] back", stIce1)
}
