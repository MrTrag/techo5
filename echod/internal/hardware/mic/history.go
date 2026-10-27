package mic

import (
	"math"
	"sync"
	"time"
)

// History is how much recent audio a turn sends before its live frames. Detection can only fire
// once the wake word has been spoken, and speech runs straight on into the request, so without this
// the first syllables of "turn on the kitchen light" are gone before anything is streaming.
//
// It is a compromise in both directions: too little and the first word of the request is missing,
// too much and the tail of the wake word is transcribed as part of the request, which can stop it
// matching an intent.
const History = 250 * time.Millisecond

// Confirming is how much a wake the device was not sure of sends ahead of it when Home Assistant is asked
// to confirm it: the whole word and what led up to it, which is what a wake word engine needs to hear to
// score the word at all.
const Confirming = 2 * time.Second

// The ring holds two seconds, whatever is being sent of it. A turn sends a quarter of that; the rest
// is for the recording a turn keeps, which wants the wake word whole. Detection fires around half a
// second after the word has ended, and the word itself is half a second or more, so a ring of one
// second held its tail and lost its start (measured on the lounge Dot, 24 September 2026). 64 KB.
const historySamples = 2 * Rate

// historyFrames is the same two seconds counted in frames, for the loudness kept beside the samples.
const historyFrames = historySamples / FrameSamples

// history is a ring of the most recently captured mono samples.
//
// Beside it, one number a frame for how loud that frame was: once as it came off the microphones and
// once as it went into the ring, after leveling. The second is what the wake models heard; the
// first is the room, before any gain was put on it. The two together are how a word said in this
// room is told from the same word heard through a wall and lifted by the leveler (see Loudness).
type history struct {
	mu     sync.Mutex
	buf    [historySamples]int16
	at     int
	filled bool

	// Mean square of each frame, before and after leveling, oldest overwritten first. frameAt is
	// where the next frame goes and frames how many are held, capped at the ring's size.
	before, after [historyFrames]float64
	frameAt       int
	frames        int
}

func (h *history) add(frame []int16, before float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, s := range frame {
		h.buf[h.at] = s
		h.at++
		if h.at == len(h.buf) {
			h.at, h.filled = 0, true
		}
	}

	h.before[h.frameAt], h.after[h.frameAt] = before, meanSquare(frame)
	h.frameAt = (h.frameAt + 1) % historyFrames
	h.frames = min(h.frames+1, historyFrames)
}

// meanSquare is a frame's power against full scale, which is what a level in dBFS is made from.
func meanSquare(frame []int16) float64 {
	if len(frame) == 0 {
		return 0
	}
	var sum float64
	for _, s := range frame {
		v := float64(s) / fullScale
		sum += v * v
	}
	return sum / float64(len(frame))
}

// WordWindow is how much audio a word's loudness is measured over: the loudest stretch this long in
// the audio before a wake. About the length of a stressed syllable, so it finds the word rather than
// averaging it with the silence either side.
const WordWindow = 240 * time.Millisecond

// Loudness is how loud the wake word was, in dBFS, measured over the loudest WordWindow of the audio
// before a detection.
type Loudness struct {
	// Heard is the word as the wake models heard it and as a recording holds it, after leveling.
	Heard float64
	// Raw is the same stretch before leveling: the word as it reached the microphones. Heard minus
	// Raw is the gain the leveler had on at the time.
	Raw float64
	// Floor is the room's own noise as the leveler tracks it (Source.Floor), for telling a quiet
	// word in a quiet room from one in a noisy room.
	Floor float64
}

// silentDB stands for a level with no power in it at all, where the logarithm has nothing to say.
const silentDB = -120.0

func dbOf(power float64) float64 {
	if power <= 0 {
		return silentDB
	}
	return max(10*math.Log10(power), silentDB)
}

// loudest finds the loudest WordWindow in the last d of frames, as it was heard, and reports that
// stretch both as heard and as it came in. Both are measured over the same frames so their difference
// is the gain rather than two unrelated moments.
func (h *history) loudest(d time.Duration) (heard, raw float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	span := min(int(d/(time.Second/(Rate/FrameSamples))), h.frames)
	window := max(int(WordWindow/(time.Second/(Rate/FrameSamples))), 1)
	if span == 0 {
		return silentDB, silentDB
	}
	window = min(window, span)

	// Oldest first over the span, so a window is a run of consecutive frames.
	start := (h.frameAt - span + historyFrames) % historyFrames
	at := func(i int) int { return (start + i) % historyFrames }

	var sumAfter, sumBefore float64
	for i := range window {
		sumAfter += h.after[at(i)]
		sumBefore += h.before[at(i)]
	}
	bestAfter, bestBefore := sumAfter, sumBefore
	for i := window; i < span; i++ {
		sumAfter += h.after[at(i)] - h.after[at(i-window)]
		sumBefore += h.before[at(i)] - h.before[at(i-window)]
		if sumAfter > bestAfter {
			bestAfter, bestBefore = sumAfter, sumBefore
		}
	}
	return dbOf(bestAfter / float64(window)), dbOf(bestBefore / float64(window))
}

// recent returns up to d of the most recent audio, oldest first.
func (h *history) recent(d time.Duration) []int16 {
	want := int(d/time.Millisecond) * Rate / 1000

	h.mu.Lock()
	defer h.mu.Unlock()

	have := len(h.buf)
	if !h.filled {
		have = h.at
	}
	want = min(want, have)

	out := make([]int16, 0, want)
	// Walk back from the write position, wrapping, then hand it over oldest first.
	start := h.at - want
	if start < 0 {
		start += len(h.buf)
	}
	for i := range want {
		out = append(out, h.buf[(start+i)%len(h.buf)])
	}
	return out
}

// remember keeps a frame as it went out, with how loud it was before leveling touched it.
func (s *Source) remember(frame []int16, before float64) { s.history.add(frame, before) }

// Loudness is how loud the loudest WordWindow of the last d was, as heard and as it came in, and how
// loud the room is. Asked at a wake, it says how loud the wake word was.
func (s *Source) Loudness(d time.Duration) Loudness {
	heard, raw := s.history.loudest(d)
	l := Loudness{Heard: heard, Raw: raw, Floor: silentDB}
	if s.leveler != nil {
		l.Floor = s.Floor()
	}
	return l
}

// Recent returns up to d of the audio captured before now, oldest first. A turn sends this ahead of
// its live frames so the speech that ran on from the wake word is not lost.
func (s *Source) Recent(d time.Duration) []int16 { return s.history.recent(d) }

// ListenHistory subscribes and copies up to d of recent audio at the same capture
// boundary. The first live frame follows the history without a gap or overlap.
// Live delivery has the same bounded queue and drop policy as Listen.
func (s *Source) ListenHistory(name string, d time.Duration) (<-chan []int16, []int16, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	frames, unlisten := s.listenLocked(name)
	var pre []int16
	if d > 0 {
		pre = s.Recent(d)
	}
	return frames, pre, unlisten
}
