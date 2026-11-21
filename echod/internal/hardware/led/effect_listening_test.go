package led

import (
	"slices"
	"testing"
	"time"
)

// pickedOut is the segment showing the most light, and whether any segment differs from the rest.
func pickedOut(frame []Color) (int, bool) {
	sum := func(c Color) int { return int(c.R) + int(c.G) + int(c.B) }
	best, differs := 0, false
	for i, c := range frame {
		if c != frame[0] {
			differs = true
		}
		if sum(c) > sum(frame[best]) {
			best = i
		}
	}
	return best, differs
}

// The whole ring is on from the first frame, before anything is known about where the voice is: the
// light is what says the word was heard, and that cannot wait for a direction.
func TestListeningLightsTheWholeRingAtOnce(t *testing.T) {
	f, err := effect(EffectListening, HomeAssistant, Room{
		Level:  func() float64 { return 0 },
		Facing: func() (float64, bool) { return 0, false },
	})
	if err != nil {
		t.Fatal(err)
	}

	frame := f(0)
	if len(frame) != Segments {
		t.Fatalf("%d segments, want %d", len(frame), Segments)
	}
	for i, c := range frame {
		if c == (Color{}) {
			t.Errorf("segment %d is dark on the first frame", i)
		}
	}
	if _, differs := pickedOut(frame); differs {
		t.Error("a side is picked out before any voice was heard")
	}
}

// It brightens toward the voice once, and then holds: a later sound from somewhere else moves nothing,
// because a ring that keeps changing is a ring that can be heard through the speaker.
func TestListeningPointsAtTheVoiceAndThenHoldsStill(t *testing.T) {
	side := 0.25 // a quarter of the way round: segment 3
	loud := 0.8

	f, err := effect(EffectListening, HomeAssistant, Room{
		Level:  func() float64 { return loud },
		Facing: func() (float64, bool) { return side, true },
	})
	if err != nil {
		t.Fatal(err)
	}

	var settled []Color
	for i := range 10 {
		settled = f(time.Duration(i) * FrameInterval)
	}
	at, differs := pickedOut(settled)
	if !differs {
		t.Fatal("no side was picked out after ten frames of a voice")
	}
	if at != 3 {
		t.Errorf("pickedOut at segment %d, want 3", at)
	}

	side = 0.75
	for i := 10; i < 60; i++ {
		if got := f(time.Duration(i) * FrameInterval); !slices.Equal(got, settled) {
			t.Fatalf("frame %d changed after the direction was settled", i)
		}
	}
}

// Silence is not a direction. A turn with nothing said never gets a side picked out, however long it
// lasts and whatever the room does after the looking has stopped.
func TestListeningPicksNoSideInSilence(t *testing.T) {
	loud := 0.0

	f, err := effect(EffectListening, HomeAssistant, Room{
		Level:  func() float64 { return loud },
		Facing: func() (float64, bool) { return 0.5, true },
	})
	if err != nil {
		t.Fatal(err)
	}

	for i := range 150 {
		if _, differs := pickedOut(f(time.Duration(i) * FrameInterval)); differs {
			t.Fatalf("a side was picked out in silence at frame %d", i)
		}
	}

	loud = 1
	for i := 150; i < 200; i++ {
		if _, differs := pickedOut(f(time.Duration(i) * FrameInterval)); differs {
			t.Fatalf("a side was picked out at frame %d, after the looking had stopped", i)
		}
	}
}

// Only a wake word offers it: the light and the room reaction have nothing that opens a turn.
func TestListeningIsOfferedOnlyToAWakeWord(t *testing.T) {
	if !slices.Contains(WakeNames(), EffectListening) {
		t.Errorf("%q is not offered to a wake word: %v", EffectListening, WakeNames())
	}
	if slices.Contains(Names(KindLight), EffectListening) || slices.Contains(Names(KindRoom), EffectListening) {
		t.Errorf("%q is offered somewhere other than a wake word", EffectListening)
	}
	if !Senses(EffectListening) || Senses(EffectPulse) {
		t.Error("Senses does not tell an effect that reads the room from one that does not")
	}
}
