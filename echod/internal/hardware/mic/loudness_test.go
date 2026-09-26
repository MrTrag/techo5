package mic

import (
	"math"
	"testing"
	"time"
)

// steady is one frame holding the same sample throughout, so its level is exactly 20 log10(v/full).
func steady(v int16) []int16 {
	f := make([]int16, FrameSamples)
	for i := range f {
		f[i] = v
	}
	return f
}

func dbFS(v int16) float64 { return 20 * math.Log10(float64(v)/fullScale) }

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// A word is the loudest stretch of the audio before a wake, heard and as it came in over the same
// frames, so the difference between the two is the gain the leveler had on the word. The quiet
// either side of it does not drag the number down.
func TestLoudnessFindsTheWordAndTheGainOnIt(t *testing.T) {
	var h history
	for i := range historyFrames {
		heard, came := int16(100), int16(100)
		if i >= 40 && i < 52 { // 240 ms of word, lifted about 26 dB by the leveler
			heard, came = 10000, 500
		}
		h.add(steady(heard), meanSquare(steady(came)))
	}

	heard, raw := h.loudest(2 * time.Second)
	if !near(heard, dbFS(10000)) || !near(raw, dbFS(500)) {
		t.Errorf("loudest is heard %.2f raw %.2f dBFS, want %.2f and %.2f", heard, raw, dbFS(10000), dbFS(500))
	}
}

// Only the span asked for counts: a loud word older than that is not this wake's word.
func TestLoudnessLooksOnlyAsFarBackAsAsked(t *testing.T) {
	var h history
	for i := range historyFrames {
		v := int16(100)
		if i < 12 {
			v = 10000
		}
		h.add(steady(v), meanSquare(steady(v)))
	}

	if heard, _ := h.loudest(time.Second); !near(heard, dbFS(100)) {
		t.Errorf("the last second reads %.2f dBFS, want %.2f: the loud start is outside it", heard, dbFS(100))
	}
	if heard, _ := h.loudest(2 * time.Second); !near(heard, dbFS(10000)) {
		t.Errorf("two seconds read %.2f dBFS, want %.2f", heard, dbFS(10000))
	}
}

// The levels ride the same ring as the samples: after more than two seconds only the newest two
// seconds are there, and before anything at all the answer is silence, not a panic or zero dBFS.
func TestLoudnessWrapsWithTheRingAndStartsSilent(t *testing.T) {
	var h history
	if heard, raw := h.loudest(2 * time.Second); heard != silentDB || raw != silentDB {
		t.Errorf("an empty history reads %v / %v, want %v", heard, raw, silentDB)
	}

	for range historyFrames {
		h.add(steady(20000), meanSquare(steady(20000)))
	}
	for range historyFrames + 7 {
		h.add(steady(300), meanSquare(steady(300)))
	}
	if h.frames != historyFrames {
		t.Errorf("holds %d frames, want %d", h.frames, historyFrames)
	}
	if heard, _ := h.loudest(2 * time.Second); !near(heard, dbFS(300)) {
		t.Errorf("reads %.2f dBFS, want %.2f: the loud frames were overwritten", heard, dbFS(300))
	}

	// A source that has never run, as the detect tests make, answers too.
	if l := (&Source{}).Loudness(2 * time.Second); l.Heard != silentDB || l.Floor != silentDB {
		t.Errorf("a source with no frames reads %+v, want silence throughout", l)
	}
}
