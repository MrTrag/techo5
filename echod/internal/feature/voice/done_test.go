package voice

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
)

func TestIsDone(t *testing.T) {
	for _, s := range []string{"Done", "done", "Done.", " DONE! ", "Done..."} {
		if !isDone(s) {
			t.Errorf("%q is not taken as the done word", s)
		}
	}
	for _, s := range []string{"", "Done, the light is off.", "Not done", "Turned off the light", "Doner"} {
		if isDone(s) {
			t.Errorf("%q is taken as the done word", s)
		}
	}
}

// ownSounds points the speaker at a folder of the test's own, with a quarter second of done.wav in
// it when done is set.
func ownSounds(t *testing.T, done bool) {
	t.Helper()
	was := speaker.SoundsDir
	speaker.SoundsDir = t.TempDir()
	t.Cleanup(func() { speaker.SoundsDir = was })
	if !done {
		return
	}
	frames := speaker.Rate / 4
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+frames*2))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&b, binary.LittleEndian, uint16(1)) // mono
	binary.Write(&b, binary.LittleEndian, uint32(speaker.Rate))
	binary.Write(&b, binary.LittleEndian, uint32(speaker.Rate*2))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(frames*2))
	for range frames {
		binary.Write(&b, binary.LittleEndian, int16(8000))
	}
	if err := os.WriteFile(filepath.Join(speaker.SoundsDir, "done.wav"), b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// "Done" on a device with a recording of the owner's for it plays the recording in the reply's place:
// the turn is replying the moment the words arrive, the reply Home Assistant sends for them - its url,
// its stream - goes unplayed, the run ending does not end the turn under it, and the turn is over
// once the recording has been heard.
func TestDoneWithARecordingPlaysTheRecording(t *testing.T) {
	ownSounds(t, true)
	c := thinkingTurn(t)
	url, _ := heldSpeech(t)

	c.handle(event{kind: evReplyText, text: "Done."})
	if c.phase != phaseReplying || !c.reply.own {
		t.Fatalf("after the words the turn is %v, own %v", c.phase, c.reply.own)
	}
	c.handle(event{kind: evStreamStart})
	c.handle(event{kind: evReplyURL, url: url})
	c.handle(event{kind: evStreamAudio, audio: make([]byte, 64)})
	if c.reply.url != "" || c.reply.stream != nil || !c.reply.own {
		t.Fatalf("Home Assistant's reply was taken up: url %q, stream %v, own %v", c.reply.url, c.reply.stream != nil, c.reply.own)
	}
	c.handle(event{kind: evRunEnd})
	if c.phase != phaseReplying {
		t.Fatalf("the run ending ended the turn while the recording plays: %v", c.phase)
	}
	// The speaker calls the recording heard once its queue has stayed dry for a while (driver.await).
	played := next(t, c, evPlayed)
	c.handle(played)
	if c.phase == phaseReplying {
		t.Fatalf("the turn is still replying after the recording was heard")
	}
}

// Without a recording the word is a reply like any other, spoken as Home Assistant sends it.
func TestDoneWithoutARecordingIsSpoken(t *testing.T) {
	ownSounds(t, false)
	c := thinkingTurn(t)

	c.handle(event{kind: evReplyText, text: "Done."})
	if c.reply.own || c.phase == phaseReplying {
		t.Fatalf("with no recording the turn is %v, own %v", c.phase, c.reply.own)
	}
}
