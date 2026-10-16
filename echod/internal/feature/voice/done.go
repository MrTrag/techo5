package voice

// A command carried out is answered with the one word "Done" (jarvis): Home Assistant's replies for
// its control intents say it, from a custom_sentences file, and a device with a recording of its own
// for it - done.wav among the sounds of your own - plays that in the reply's place, the instant the
// words arrive, with no voice synthesized and nothing fetched. Home Assistant goes on to send the
// spoken reply as it always does, as a url and as a stream; both arrive and go unplayed. Without the
// recording the word is spoken as any reply is.

import (
	"context"
	"log/slog"
	"strings"
	"unicode"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
)

// doneWord is the reply that stands for a command carried out.
const doneWord = "done"

// isDone reports whether text is the done word alone, however it is cased or punctuated: "Done",
// "Done.", "done!". A sentence that only starts with it is a reply to be spoken.
func isDone(text string) bool {
	word := strings.TrimFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) })
	return word == doneWord
}

// sayDone plays note, the owner's recording for a command carried out, as the turn's reply.
func (c *conversation) sayDone(note speaker.Note) {
	slog.Info("replying with the owner's recording", "slot", c.slot+1, "sound", "done")
	c.respond(reply{own: true}, func(ctx context.Context, p *speaker.Player, playing func()) error {
		playing()
		p.Chime(speaker.ToneLevel, note)
		return nil
	})
}
