package wakeword

// The named wake sound (jarvis): "Yes, Steve?" rather than "Yes?".
//
// Who said the wake word is known only to the house's voice-ID service, which compares the word
// against prints made from the same word, so a turn the device's own engine opened asks it: the two
// seconds of microphone that hold the word go to config.Wake.VoiceCheck as raw 16-bit audio, and the
// answer is a name. The owner's recording for that name, wake_<name>.wav among the sounds of their
// own, then plays in the wake sound's place. Anything else - no answer in time, "neither", a name
// with no recording - plays the ordinary wake sound, so the check can only ever add the name.
//
// The check takes a few tens of milliseconds on the server. The wait is bounded (VoiceCheckMs) and
// the wake sound is held back until the answer or the deadline, whichever is first: the device
// listens only once the sound has played, so a slow answer would otherwise cut into the request.
//
// The question is put as the word is detected (Ask, from detect's OnDetect) and the answer collected
// when the turn opens (Who). Between the two the devices that heard the word may spend a window
// agreeing which of them answers (feature/nearest); the check runs inside that window rather than
// after it, so the two waits do not add up.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
)

// DefaultVoiceCheckWait is how long a wake waits for the answer unless config.Wake.VoiceCheckMs says.
const DefaultVoiceCheckWait = 250 * time.Millisecond

var checkClient = &http.Client{}

// asked is the question put at detection (Ask), for Who to collect the answer of. One at a time: a
// wake word detected before the last one's answer was collected replaces it.
var asked struct {
	sync.Mutex
	slot int
	at   time.Time
	done chan string
}

// askedFor is how long an answer is kept for: longer than the longest nearest window and the check's
// own wait together, and short enough that a wake word that never opened a turn is not answered for
// by the next.
const askedFor = 2 * time.Second

// Ask puts the question as the wake word in slot is detected, before the devices that heard it agree
// which answers, so the answer is in by the time the wake sound is due (Who). history is the two
// seconds of microphone that hold the word. Nothing is asked when the check is off, no named sound
// could play, or the slot makes no sound, so it costs nothing then.
func Ask(slot int, history []int16) {
	url, wait, ok := check(history)
	if !ok || !Tones(slot, false) {
		return
	}
	ask(slot, url, wait, history)
}

// ask puts the question for slot, in the background, keeping the answer for collect.
func ask(slot int, url string, wait time.Duration, history []int16) {
	done := make(chan string, 1)
	asked.Lock()
	asked.slot, asked.at, asked.done = slot, time.Now(), done
	asked.Unlock()
	go func() { done <- who(url, wait, history) }()
}

// Who is who said the wake word in slot: the lower-case name to play, or "" when there is no name to
// play (the check is off, there is no named sound to play, no answer came in time, or the answer has
// no recording). The answer to a question Ask put for this slot is waited for, which is usually no
// wait at all; with none put, the question is asked now, of history (16 kHz mono).
func Who(slot int, history []int16) string {
	url, wait, ok := check(history)
	if !ok {
		return ""
	}
	if name, put := collect(slot, wait); put {
		return name
	}
	return who(url, wait, history)
}

// collect is the answer to the question ask put for slot, and whether one was put: none, one for
// another slot, or one too old to be this wake word's is no question. An answer is collected once.
func collect(slot int, wait time.Duration) (name string, put bool) {
	asked.Lock()
	done, at, ours := asked.done, asked.at, asked.slot == slot
	asked.done = nil
	asked.Unlock()
	if done == nil || !ours || time.Since(at) >= askedFor {
		return "", false
	}
	// who gives up within wait of the question, so this is a bound on a wait that is already over.
	select {
	case name = <-done:
		return name, true
	case <-time.After(wait):
		slog.Info("wake word voice check: the answer asked for at the word never came", "ms", time.Since(at).Milliseconds())
		return "", true
	}
}

// check is whether a check is worth making, and where and for how long: the check is on, there is
// audio to send, and a named sound exists to play.
func check(history []int16) (url string, wait time.Duration, ok bool) {
	w := config.Get().Wake
	url = w.VoiceCheckURL()
	if url == "" || len(history) == 0 || !speaker.AnyWakeNamed() {
		return "", 0, false
	}
	wait = time.Duration(w.VoiceCheckMs) * time.Millisecond
	if wait <= 0 {
		wait = DefaultVoiceCheckWait
	}
	return url, wait, true
}

func who(url string, wait time.Duration, history []int16) string {
	t := time.Now()
	body := make([]byte, 2*len(history))
	for i, s := range history {
		body[2*i], body[2*i+1] = byte(s), byte(s>>8)
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "audio/L16; rate=16000")
	if h, err := os.Hostname(); err == nil {
		req.Header.Set("X-Device", h)
	}
	resp, err := checkClient.Do(req)
	if err != nil {
		slog.Info("wake word voice check: no answer in time", "ms", time.Since(t).Milliseconds(), "err", err)
		return ""
	}
	defer resp.Body.Close()
	var out struct {
		Speaker string             `json:"speaker"`
		Scores  map[string]float64 `json:"scores"`
		CheckMs int                `json:"check_ms"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out) != nil {
		slog.Info("wake word voice check: bad answer", "status", resp.StatusCode, "ms", time.Since(t).Milliseconds())
		return ""
	}
	name := strings.ToLower(out.Speaker)
	_, named := speaker.WakeNamed(name)
	slog.Info("wake word voice check", "who", out.Speaker, "scores", out.Scores, "named", named,
		"ms", time.Since(t).Milliseconds(), "check_ms", out.CheckMs)
	if !named {
		return ""
	}
	return name
}

// ChimeNamed sounds a detection: the owner's recording for name when there is one, else as Chime.
func ChimeNamed(slot int, followUp bool, name string) {
	if !Tones(slot, followUp) {
		return
	}
	if n, ok := speaker.WakeNamed(name); ok {
		speaker.Sound().Chime([]speaker.Note{n})
		return
	}
	speaker.Sound().Chime(speaker.WakeTone(toneFor(slot, followUp)))
}

// ChimeLengthNamed is ChimeLength for a detection that sounded as ChimeNamed(slot, followUp, name).
func ChimeLengthNamed(slot int, followUp bool, name string) time.Duration {
	if n, ok := speaker.WakeNamed(name); ok {
		return speaker.Audible([]speaker.Note{n}, !config.Get().Microphone.Cancel)
	}
	return ChimeLength(slot, followUp)
}
