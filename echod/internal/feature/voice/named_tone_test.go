package voice

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
)

// A named wake sound is not the slot's tone (jarvis): the turn's end of speech detector stays deaf for
// the sound that actually played, or it would start listening with a name still coming out of the
// speaker.
func TestToneEndsGoesByTheNamedSound(t *testing.T) {
	stepping(t)
	ownSounds(t, true)
	if err := os.Rename(filepath.Join(speaker.SoundsDir, "done.wav"), filepath.Join(speaker.SoundsDir, "wake_steve.wav")); err != nil {
		t.Fatal(err)
	}

	named, plain := toneEnds(0, false, "steve"), toneEnds(0, false, "")
	if named == plain {
		t.Errorf("the named sound lasts as long as the tone, %v: its own length was not used", named)
	}
	if most := 250*time.Millisecond + speaker.HardwareTail; named <= speaker.HardwareTail || named > most {
		t.Errorf("a quarter second of named sound ends after %v, want at most %v", named, most)
	}
}
