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

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
)

// DefaultVoiceCheckWait is how long a wake waits for the answer unless config.Wake.VoiceCheckMs says.
const DefaultVoiceCheckWait = 250 * time.Millisecond

var checkClient = &http.Client{}

// Who asks who said the wake word in history (16 kHz mono) and reports the lower-case name, or ""
// when there is no name to play: the check is off, there is no named sound to play, no answer came
// in time, or the answer has no recording.
func Who(history []int16) string {
	w := config.Get().Wake
	url := w.VoiceCheckURL()
	if url == "" || len(history) == 0 || !speaker.AnyWakeNamed() {
		return ""
	}
	wait := time.Duration(w.VoiceCheckMs) * time.Millisecond
	if wait <= 0 {
		wait = DefaultVoiceCheckWait
	}
	return who(url, wait, history)
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
