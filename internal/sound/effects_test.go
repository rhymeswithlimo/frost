package sound

import (
	"math"
	"testing"
	"time"
)

func TestShiftRaisesPitchAndShortens(t *testing.T) {
	s := sine(400, 44100)
	out := effects(s, Clip{Shift: 2})
	if len(out) != len(s)/2 {
		t.Fatalf("length %d, want %d", len(out), len(s)/2)
	}
	if p := pitchOf(out); !near(p, 800, 0.02) {
		t.Fatalf("pitch %.0f, want about 800", p)
	}
}

func TestRingMovesEnergyToTheCarrier(t *testing.T) {
	// A 100 Hz tone ring-modulated at 1500 Hz comes out at 1400 and 1600 Hz:
	// its zero crossings average around the carrier.
	out := effects(sine(100, 44100), Clip{Ring: 1500})
	if p := pitchOf(out); !near(p, 1500, 0.1) {
		t.Fatalf("ring-modulated pitch %.0f, want near 1500", p)
	}
}

func TestCrushQuantises(t *testing.T) {
	out := effects(sine(440, 4410), Clip{Crush: 3})
	levels := map[float32]bool{}
	for _, v := range out {
		levels[v] = true
	}
	if len(levels) > 9 { // 3 bits: -4/4 ... 4/4
		t.Fatalf("3-bit crush left %d distinct levels", len(levels))
	}
}

func TestDecayFades(t *testing.T) {
	s := make([]float32, 44100)
	for i := range s {
		s[i] = 1
	}
	out := effects(s, Clip{Decay: 50 * time.Millisecond})
	at := func(d time.Duration) float64 { return float64(out[int(d.Seconds()*sampleRate)]) }
	if math.Abs(at(50*time.Millisecond)-math.Exp(-1)) > 0.01 {
		t.Fatalf("level after one decay time = %.3f, want %.3f", at(50*time.Millisecond), math.Exp(-1))
	}
	if at(500*time.Millisecond) > 0.001 {
		t.Fatal("decayed clip should be near silent after ten decay times")
	}
}

func TestEffectsLeaveOriginalAlone(t *testing.T) {
	s := sine(440, 4410)
	before := append([]float32(nil), s...)
	effects(s, Clip{Ring: 1000, Crush: 4, Decay: time.Millisecond})
	for i := range s {
		if s[i] != before[i] {
			t.Fatal("effects modified the decoded samples")
		}
	}
}
