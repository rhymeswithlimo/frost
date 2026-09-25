package sound

import (
	"math"
	"testing"
	"time"
)

// sine makes n samples of a tone at freq Hz.
func sine(freq float64, n int) []float32 {
	s := make([]float32, n)
	for i := range s {
		s[i] = float32(0.8 * math.Sin(2*math.Pi*freq*float64(i)/sampleRate))
	}
	return s
}

// pitchOf estimates the frequency of a tone by counting upward zero crossings
// in the middle of the clip, away from the edges.
func pitchOf(s []float32) float64 {
	a, b := len(s)/5, len(s)*4/5
	crossings := 0
	for i := a + 1; i < b; i++ {
		if s[i-1] < 0 && s[i] >= 0 {
			crossings++
		}
	}
	return float64(crossings) * sampleRate / float64(b-a)
}

func near(got, want, tol float64) bool { return math.Abs(got-want) <= want*tol }

func TestVaryUnchanged(t *testing.T) {
	s := sine(440, 20000)
	out := vary(s, 1, 1)
	if len(out) != len(s) {
		t.Fatalf("length %d, want %d", len(out), len(s))
	}
	for i := range s {
		if out[i] != s[i] {
			t.Fatal("pitch 1, tempo 1 changed the samples")
		}
	}
}

func TestPitchWithoutTempo(t *testing.T) {
	s := sine(440, 22050)
	out := vary(s, 1.05, 1)
	if !near(float64(len(out)), float64(len(s)), 0.01) {
		t.Errorf("pitch-only change altered length: %d vs %d", len(out), len(s))
	}
	if p := pitchOf(out); !near(p, 440*1.05, 0.015) {
		t.Errorf("pitch = %.1f Hz, want about %.1f", p, 440*1.05)
	}
}

func TestTempoWithoutPitch(t *testing.T) {
	s := sine(440, 22050)
	out := vary(s, 1, 1.05)
	if !near(float64(len(out)), float64(len(s))/1.05, 0.01) {
		t.Errorf("length %d, want about %.0f", len(out), float64(len(s))/1.05)
	}
	if p := pitchOf(out); !near(p, 440, 0.015) {
		t.Errorf("tempo-only change moved pitch to %.1f Hz", p)
	}
}

func TestPitchAndTempoTogether(t *testing.T) {
	s := sine(330, 22050)
	out := vary(s, 0.97, 1.03)
	if !near(float64(len(out)), float64(len(s))/1.03, 0.01) {
		t.Errorf("length %d, want about %.0f", len(out), float64(len(s))/1.03)
	}
	if p := pitchOf(out); !near(p, 330*0.97, 0.015) {
		t.Errorf("pitch = %.1f Hz, want about %.1f", p, 330*0.97)
	}
}

func TestJitterStaysSubtle(t *testing.T) {
	const amount = 0.03
	sum, n := 0.0, 20000
	within := 0
	for range n {
		j := jitter(amount)
		if j < 1-amount || j > 1+amount {
			t.Fatalf("jitter(%v) = %v, out of range", amount, j)
		}
		if math.Abs(j-1) < amount/2 {
			within++
		}
		sum += j
	}
	if mean := sum / float64(n); math.Abs(mean-1) > 0.002 {
		t.Errorf("jitter is biased: mean %v", mean)
	}
	// Triangular: three quarters of plays land in the middle half of the range.
	if frac := float64(within) / float64(n); frac < 0.7 || frac > 0.8 {
		t.Errorf("%.2f of plays in the middle half, want about 0.75", frac)
	}
	if jitter(0) != 1 {
		t.Error("zero variation should play the clip as recorded")
	}
}

func TestShortClipsStillVary(t *testing.T) {
	// Too short to stretch: pitch still moves, nothing panics.
	s := sine(440, 1500)
	if out := vary(s, 1.02, 1.02); len(out) == 0 {
		t.Fatal("short clip vanished")
	}
	if out := vary(nil, 1.02, 0.98); len(out) != 0 {
		t.Fatal("empty clip grew")
	}
}

func TestCutShortensAndFades(t *testing.T) {
	s := make([]float32, 44100) // one second at full level
	for i := range s {
		s[i] = 1
	}
	out := cut(s, 150*time.Millisecond)
	if want := int(0.15 * sampleRate); len(out) != want {
		t.Fatalf("cut length %d, want %d", len(out), want)
	}
	if out[0] != 1 || out[len(out)/2] != 1 {
		t.Fatal("cut changed the start of the clip")
	}
	if out[len(out)-1] > 0.01 {
		t.Fatalf("cut clip ends at %v, want a fade to silence", out[len(out)-1])
	}
	if s[len(out)-1] != 1 {
		t.Fatal("cut modified the original samples")
	}
	if got := cut(s, 0); len(got) != len(s) {
		t.Fatal("no cut should keep the whole clip")
	}
	if got := cut(s, 5*time.Second); len(got) != len(s) {
		t.Fatal("a cut longer than the clip should keep it whole")
	}
}
