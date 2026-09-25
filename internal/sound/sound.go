// Package sound plays short sound effects. It's only used by the TUI's
// hidden game, and it only touches the audio device once a Player is made.
//
// Playback goes through oto, which is pure Go on every platform frost ships
// for (Core Audio on macOS, WASAPI on Windows, PulseAudio or ALSA loaded at
// runtime on Linux). If there's no audio device, Play does nothing.
//
// Every play is varied a little, pitch and speed separately, so repeated
// effects don't sound mechanical. See vary.go.
package sound

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
)

const (
	sampleRate  = 44100
	maxPerClip  = 4    // overlapping copies of one clip, so rapid fire can't pile up
	clipVolume  = 0.36 // effects shouldn't be startling
	disabledEnv = "FROST_NO_SOUND"
)

// oto allows one context per process, so it's shared.
var (
	ctxOnce sync.Once
	ctx     *oto.Context
	ctxErr  error
	ctxWait chan struct{}
)

func context() (*oto.Context, chan struct{}, error) {
	ctxOnce.Do(func() {
		ctx, ctxWait, ctxErr = oto.NewContext(&oto.NewContextOptions{
			SampleRate:   sampleRate,
			ChannelCount: 1,
			Format:       oto.FormatFloat32LE,
		})
	})
	return ctx, ctxWait, ctxErr
}

// Clip is a sound effect and how much it may vary each time it plays.
type Clip struct {
	WAV []byte
	// Pitch and Tempo are the largest random change per play, as a
	// fraction: 0.03 means up to 3% higher or lower, or faster or slower.
	// Zero plays the clip exactly as recorded.
	Pitch, Tempo float64
	// Volume scales this clip against the others. Zero means 1.
	Volume float64
	// Cut, if set, stops the clip after this long, with a short fade so it
	// doesn't click.
	Cut time.Duration

	// Effects that turn one recording into a different sound. They're
	// applied once, when the Player is made, in this order.
	Shift float64       // fixed pitch factor: 2 is an octave up, shorter too
	Ring  float64       // ring modulation frequency in Hz: metallic, bell-like
	Crush int           // bit depth, 1 to 15: gritty, digital
	Decay time.Duration // exponential fade from the start: struck, not held
}

type clip struct {
	samples      []float32 // mono, 44.1 kHz, already cut
	pitch, tempo float64
	volume       float64
}

// fadeOut is how long a cut clip takes to fade to silence.
const fadeOut = 20 * time.Millisecond

// cut shortens s to d and fades out the last few milliseconds.
func cut(s []float32, d time.Duration) []float32 {
	n := int(d.Seconds() * sampleRate)
	if d <= 0 || n >= len(s) {
		return s
	}
	out := append([]float32(nil), s[:n]...)
	fade := min(n, int(fadeOut.Seconds()*sampleRate))
	for i := range fade {
		out[n-fade+i] *= float32(fade-i) / float32(fade)
	}
	return out
}

// Player plays named clips.
type Player struct {
	clips map[string]clip // read-only after New

	mu     sync.Mutex
	ready  bool
	err    error
	active map[string][]*oto.Player
}

// New decodes the given clips and starts opening the audio device in the
// background, so the caller never waits on it. Setting FROST_NO_SOUND turns
// sound off entirely.
func New(clips map[string]Clip) *Player {
	p := &Player{clips: map[string]clip{}, active: map[string][]*oto.Player{}}
	if os.Getenv(disabledEnv) != "" {
		p.err = errors.New("sound disabled by " + disabledEnv)
		return p
	}
	for name, c := range clips {
		samples, err := decodeWAV(c.WAV)
		if err != nil {
			p.err = fmt.Errorf("%s: %w", name, err)
			return p
		}
		vol := c.Volume
		if vol == 0 {
			vol = 1
		}
		samples = cut(effects(samples, c), c.Cut)
		p.clips[name] = clip{samples: samples, pitch: c.Pitch, tempo: c.Tempo, volume: vol}
	}
	go func() {
		c, wait, err := context()
		if err == nil {
			<-wait
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if err != nil || c == nil {
			p.err = fmt.Errorf("no audio device: %v", err)
			return
		}
		p.ready = true
	}()
	return p
}

// Available reports whether sound can play. It's false until the device is
// open, and stays false if there isn't one.
func (p *Player) Available() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ready
}

// Err says why sound isn't available, if it isn't.
func (p *Player) Err() error {
	if p == nil {
		return errors.New("no sound player")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Play starts a clip, slightly varied, and returns straight away. Unknown clips, a missing
// device, or too many overlapping copies are silently ignored.
func (p *Player) Play(name string) {
	if p == nil {
		return
	}
	c, ok := p.clips[name]
	if !ok || !p.Available() {
		return
	}
	// Varying a clip takes a millisecond or two, so it happens off the
	// caller's goroutine and never stalls the UI.
	go func() {
		p.start(name, encode(vary(c.samples, jitter(c.pitch), jitter(c.tempo))), c.volume)
	}()
}

func (p *Player) start(name string, pcm []byte, volume float64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Drop finished players, keep the rest alive until they're done.
	live := p.active[name][:0]
	for _, pl := range p.active[name] {
		if pl.IsPlaying() {
			live = append(live, pl)
		}
	}
	if len(live) >= maxPerClip {
		p.active[name] = live
		return
	}
	pl := ctx.NewPlayer(bytes.NewReader(pcm))
	pl.SetVolume(clipVolume * volume)
	pl.Play()
	p.active[name] = append(live, pl)
}

// decodeWAV reads a PCM WAV file (8-bit unsigned or 16-bit signed, any
// channel count, any sample rate) and returns mono samples at 44.1 kHz.
func decodeWAV(b []byte) ([]float32, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("not a WAV file")
	}
	var (
		channels, bits int
		rate           int
		data           []byte
		haveFmt        bool
	)
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := b[off+8 : min(off+8+size, len(b))]
		switch id {
		case "fmt ":
			if len(body) < 16 {
				return nil, errors.New("short fmt chunk")
			}
			if f := binary.LittleEndian.Uint16(body[0:2]); f != 1 {
				return nil, fmt.Errorf("unsupported WAV encoding %d (want PCM)", f)
			}
			channels = int(binary.LittleEndian.Uint16(body[2:4]))
			rate = int(binary.LittleEndian.Uint32(body[4:8]))
			bits = int(binary.LittleEndian.Uint16(body[14:16]))
			haveFmt = true
		case "data":
			data = body
		}
		off += 8 + size + size%2 // chunks are word aligned
	}
	if !haveFmt || data == nil {
		return nil, errors.New("missing fmt or data chunk")
	}
	if channels < 1 || rate < 1 || (bits != 8 && bits != 16) {
		return nil, fmt.Errorf("unsupported WAV: %d channels, %d Hz, %d bit", channels, rate, bits)
	}

	// Mix down to mono floats.
	frameSize := channels * bits / 8
	frames := len(data) / frameSize
	mono := make([]float32, frames)
	for i := range frames {
		var sum float32
		for c := range channels {
			o := i*frameSize + c*bits/8
			if bits == 8 {
				sum += (float32(data[o]) - 128) / 128
			} else {
				sum += float32(int16(binary.LittleEndian.Uint16(data[o:]))) / 32768
			}
		}
		mono[i] = sum / float32(channels)
	}

	return resample(mono, float64(rate)/sampleRate), nil
}

// encode turns samples into the float32 little-endian bytes oto plays.
func encode(s []float32) []byte {
	out := make([]byte, len(s)*4)
	for i, v := range s {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(max(-1, min(1, v))))
	}
	return out
}

// Validate reports whether wav is a file this package can play.
func Validate(wav []byte) error {
	_, err := decodeWAV(wav)
	return err
}
