package sound

import (
	"math"
	"math/rand/v2"
)

// Playing a clip faster normally raises its pitch too. To move pitch and
// speed separately, vary first stretches the clip in time without touching
// its pitch, then resamples it, which shifts pitch and length together. The
// stretch is chosen so the final length comes out right.

// jitter returns a random factor within 1 ± amount. It's the difference of
// two uniform draws, so values bunch up near 1 and the extremes are rare.
func jitter(amount float64) float64 {
	return 1 + amount*(rand.Float64()-rand.Float64())
}

// vary returns s at pitch factor p and tempo factor t, where 1 means as
// recorded, 1.02 means 2% higher or 2% faster.
func vary(s []float32, pitch, tempo float64) []float32 {
	if pitch <= 0 || tempo <= 0 {
		return s
	}
	return resample(stretch(s, pitch/tempo), pitch)
}

// resample reads s at step times the normal speed with linear
// interpolation. step 1.05 plays 5% faster and higher, and shorter.
func resample(s []float32, step float64) []float32 {
	if len(s) == 0 || step <= 0 {
		return s
	}
	if math.Abs(step-1) < 1e-6 {
		return append([]float32(nil), s...)
	}
	n := int(float64(len(s)) / step)
	out := make([]float32, n)
	for i := range out {
		pos := float64(i) * step
		j := int(pos)
		if j+1 >= len(s) {
			out[i] = s[len(s)-1]
			continue
		}
		frac := float32(pos - float64(j))
		out[i] = s[j]*(1-frac) + s[j+1]*frac
	}
	return out
}

// Grains of about 23 ms: short enough to keep the attack of a sound tight,
// long enough to keep its pitch.
const (
	grain = 1024
	hop   = grain / 2
	// How far a grain may slide from its nominal spot to line up with the
	// previous one. About 6 ms.
	tolerance = 256
)

var window = func() []float32 {
	w := make([]float32, grain)
	for i := range w {
		w[i] = float32(0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/grain))
	}
	return w
}()

// stretch makes s factor times as long without changing its pitch. It's
// WSOLA: output is built from overlapping windowed grains of the input, and
// each grain is slid a few milliseconds so its waveform lines up with where
// the previous grain would have continued. Without that alignment the grain
// edges would drag the pitch along with the speed. Only meant for small
// changes.
func stretch(s []float32, factor float64) []float32 {
	if math.Abs(factor-1) < 1e-3 || len(s) < 2*grain+tolerance {
		return s
	}
	n := int(float64(len(s)) * factor)
	out := make([]float32, n+grain)
	weight := make([]float32, n+grain)
	last := len(s) - grain // last input position a whole grain fits at

	prev := 0
	for o := 0; o < n; o += hop {
		in := min(int(float64(o)/factor), last)
		if o > 0 {
			in = bestMatch(s, prev+hop, in, last)
		}
		for j := range grain {
			out[o+j] += s[in+j] * window[j]
			weight[o+j] += window[j]
		}
		prev = in
	}
	for i := range n {
		if weight[i] > 1e-3 {
			out[i] /= weight[i]
		}
	}
	return out[:n]
}

// bestMatch finds the input position near nominal whose start looks most
// like s[natural:], the samples that would naturally follow the previous
// grain.
func bestMatch(s []float32, natural, nominal, last int) int {
	if natural+hop > len(s) {
		return nominal
	}
	target := s[natural : natural+hop]
	best, bestScore := nominal, math.Inf(-1)
	for c := max(0, nominal-tolerance); c <= min(last, nominal+tolerance); c++ {
		var score float64
		for j := 0; j < hop; j += 2 { // every other sample is plenty
			score += float64(s[c+j] * target[j])
		}
		if score > bestScore {
			best, bestScore = c, score
		}
	}
	return best
}
