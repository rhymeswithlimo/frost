package sound

import (
	"math"
	"time"
)

// effects applies a Clip's shift, ring, crush and decay to s, in that order.
// Each one is skipped when its field is zero.
func effects(s []float32, c Clip) []float32 {
	if c.Shift > 0 && c.Shift != 1 {
		s = resample(s, c.Shift)
	} else {
		s = append([]float32(nil), s...) // never modify the decoded original
	}
	if c.Ring > 0 {
		ring(s, c.Ring)
	}
	if c.Crush > 0 && c.Crush < 16 {
		crush(s, c.Crush)
	}
	if c.Decay > 0 {
		decay(s, c.Decay)
	}
	return s
}

// ring multiplies s by a sine at freq Hz. The result has none of the
// original pitch, just sidebands around freq, which sounds metallic. It's
// scaled up again because the multiplication halves the average level.
func ring(s []float32, freq float64) {
	for i := range s {
		s[i] *= float32(1.5 * math.Sin(2*math.Pi*freq*float64(i)/sampleRate))
	}
}

// crush rounds every sample to the given number of bits.
func crush(s []float32, bits int) {
	levels := float32(int(1) << (bits - 1))
	for i := range s {
		s[i] = float32(math.Round(float64(s[i]*levels))) / levels
	}
}

// decay fades s exponentially, losing about 63% of its level every d.
func decay(s []float32, d time.Duration) {
	tau := d.Seconds() * sampleRate
	for i := range s {
		s[i] *= float32(math.Exp(-float64(i) / tau))
	}
}
