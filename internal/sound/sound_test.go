package sound

import (
	"encoding/binary"
	"math"
	"testing"
)

// wav builds a PCM WAV file in memory.
func wav(channels, rate, bits int, data []byte) []byte {
	b := []byte("RIFF\x00\x00\x00\x00WAVE")
	fmtc := make([]byte, 16)
	binary.LittleEndian.PutUint16(fmtc[0:], 1)
	binary.LittleEndian.PutUint16(fmtc[2:], uint16(channels))
	binary.LittleEndian.PutUint32(fmtc[4:], uint32(rate))
	binary.LittleEndian.PutUint32(fmtc[8:], uint32(rate*channels*bits/8))
	binary.LittleEndian.PutUint16(fmtc[12:], uint16(channels*bits/8))
	binary.LittleEndian.PutUint16(fmtc[14:], uint16(bits))
	b = append(b, "fmt \x10\x00\x00\x00"...)
	b = append(b, fmtc...)
	b = append(b, "LIST\x04\x00\x00\x00abcd"...) // an extra chunk to skip
	size := make([]byte, 4)
	binary.LittleEndian.PutUint32(size, uint32(len(data)))
	b = append(append(append(b, "data"...), size...), data...)
	return b
}

func TestDecode8BitMono(t *testing.T) {
	s, err := decodeWAV(wav(1, 44100, 8, []byte{128, 255, 0}))
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 3 || s[0] != 0 || s[1] < 0.99 || s[2] != -1 {
		t.Fatalf("samples = %v", s)
	}
}

func TestDecode16BitStereoResamples(t *testing.T) {
	// 4 stereo frames at 22050 Hz: left full, right silent, mixes to 0.5.
	data := make([]byte, 4*4)
	for i := range 4 {
		binary.LittleEndian.PutUint16(data[i*4:], uint16(int16(32767)))
	}
	s, err := decodeWAV(wav(2, 22050, 16, data))
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 8 {
		t.Fatalf("resampled to %d samples, want 8", len(s))
	}
	if math.Abs(float64(s[0])-0.5) > 0.01 {
		t.Fatalf("mixed sample = %v, want 0.5", s[0])
	}
}

func TestDecodeRejects(t *testing.T) {
	for name, b := range map[string][]byte{
		"not wav":   []byte("hello there, not audio"),
		"24 bit":    wav(1, 44100, 24, []byte{0, 0, 0}),
		"truncated": []byte("RIFF\x00\x00\x00\x00WAVE"),
	} {
		if _, err := decodeWAV(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDisabledAndNilAreSilent(t *testing.T) {
	t.Setenv("FROST_NO_SOUND", "1")
	p := New(map[string]Clip{"x": {WAV: wav(1, 44100, 8, []byte{128})}})
	if p.Available() || p.Err() == nil {
		t.Fatal("FROST_NO_SOUND didn't disable sound")
	}
	p.Play("x") // must not panic or block

	var none *Player
	none.Play("x")
	if none.Available() {
		t.Fatal("nil player reports available")
	}
}
