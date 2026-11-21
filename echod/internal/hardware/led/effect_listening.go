package led

import "time"

// listeningColors is what Listening is drawn in: a blue ring first, and then the paler patch laid over it
// on the side the voice came from. Colors of its own rather than the ring's, because the point is to be
// recognised at a glance from across a room, whatever the light happens to be set to.
var listeningColors = Palette{
	{R: 0, G: 50, B: 160},
	{R: 40, G: 170, B: 95},
}

// listening is the light for a turn that has just opened: the whole ring comes on at once, and a moment
// later one side of it brightens toward whoever is talking. Then it holds still until the turn moves on.
//
// Holding still is the point. On a Dot the amplifier is always on and a ring that animates can be heard
// through the speaker as a faint tone, so every other effect in the catalog is unusable while the device
// is listening. This one changes what it shows exactly once.
//
// The direction is not known when the wake word is heard: working it out only starts when something
// asks, and the word is over by then. So it is taken from what is said next. Each frame with a voice in
// it votes for where that voice was, louder frames counting for more, and the first direction to have
// been heard enough wins and is kept. A turn with nothing said after the word never gets a patch, which
// is the honest answer.
func listening(p Palette, r Room) Frame {
	const (
		// How loud a frame has to be, against the room's own floor, to count as a voice at all.
		heard = 0.1

		// How many voiced frames settle it: a quarter of a second of speech at 25 frames a second.
		enough = 6

		// When to stop looking. Somebody who has said nothing by now is thinking, and the direction of
		// the next loud thing in the room is not where they are.
		giveUp = 4 * time.Second

		// How wide the patch is, in segments. The array reports sixths of a circle, two segments each,
		// so anything narrower would claim a precision it does not have.
		width = 2.6
	)

	var (
		votes   [Segments]float64
		counted int
		at      float64
		settled bool
		done    bool
	)

	return func(elapsed time.Duration) []Color {
		if !done {
			if x := level(r); x >= heard {
				if want, ok := facing(r); ok {
					votes[int(want+0.5)%Segments] += x
					counted++
				}
			} else {
				// Asked anyway: asking is what keeps the direction being worked out, and the answer
				// has to be ready for the first frame that does have a voice in it.
				facing(r)
			}

			switch {
			case counted >= enough:
				best := 0
				for i, v := range votes {
					if v > votes[best] {
						best = i
					}
				}
				at, settled, done = float64(best), true, true
			case elapsed >= giveUp:
				done = true
			}
		}

		out := make([]Color, Segments)
		for i := range out {
			out[i] = p.Nth(0)
		}
		if settled {
			dot(out, at, p.Nth(1), width)
		}
		return out
	}
}
