package detect

import (
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/mic"
	"github.com/HuskerMinion/techo5/echod/internal/lib/wake"
)

// A shadowed slot's detection is handed to OnShadow with the word that heard it and the score, and
// never to OnDetect: nothing chimes and no turn opens. The other slot is judged as it always was, on
// the same frame.
func TestAShadowedSlotIsWatchedNotActedOn(t *testing.T) {
	made := fakes(t)
	e := New(2, &mic.Source{})

	fired := make(chan int, 2)
	shadowed := make(chan string, 2)
	e.OnDetect = func(slot int) { fired <- slot }
	e.OnShadow = func(slot int, id string, score float64, _ []int16) {
		if slot != 1 {
			t.Errorf("shadow reported for slot %d, want slot 2", slot+1)
		}
		if score < 0.9 {
			t.Errorf("shadow score %v, want the score that crossed", score)
		}
		shadowed <- id
	}
	e.Shadowed = func(slot int) bool { return slot == 1 }

	if err := e.Use(0, model("jarvis_r4", wake.KindMicroWakeWord)); err != nil {
		t.Fatalf("Use(0): %v", err)
	}
	if err := e.Use(1, model("jarvis_r5", wake.KindMicroWakeWord)); err != nil {
		t.Fatalf("Use(1): %v", err)
	}

	// Both over the threshold on the same frame: the live word fires, the shadow only reports.
	made[wake.KindMicroWakeWord].loaded["jarvis_r4"] = 0.95
	made[wake.KindMicroWakeWord].loaded["jarvis_r5"] = 0.95
	e.score([]int16{0}, e.source)

	select {
	case slot := <-fired:
		if slot != 0 {
			t.Errorf("slot %d fired, want only slot 1 to fire", slot+1)
		}
	case <-time.After(time.Second):
		t.Fatal("the live slot did not fire")
	}
	select {
	case id := <-shadowed:
		if id != "jarvis_r5" {
			t.Errorf("shadowed %q, want jarvis_r5", id)
		}
	case <-time.After(time.Second):
		t.Fatal("the shadowed slot did not report")
	}
	select {
	case slot := <-fired:
		t.Errorf("slot %d fired as well; a shadowed slot must never reach OnDetect", slot+1)
	case <-time.After(50 * time.Millisecond):
	}

	// The hold that follows is marked, so the peak it finds is logged as a shadow and not a detection.
	if s := e.slots[1]; !s.holding || !s.shadow {
		t.Errorf("the shadowed slot is holding=%v shadow=%v, want both", s.holding, s.shadow)
	}
	if s := e.slots[0]; !s.holding || s.shadow {
		t.Errorf("the live slot is holding=%v shadow=%v, want holding and not shadow", s.holding, s.shadow)
	}
}

// A near miss on a shadowed slot goes no further than the log. Ducking the music so the next try is
// heard is acting on the word, and a shadowed word is not acted on.
func TestANearMissOnAShadowedSlotDoesNotDuck(t *testing.T) {
	made := fakes(t)
	e := New(2, &mic.Source{})
	e.Shadowed = func(slot int) bool { return slot == 1 }
	e.OnNearMiss = func(slot int, peak float64) {
		t.Errorf("a near miss on shadowed slot %d reached OnNearMiss with peak %v", slot+1, peak)
	}

	if err := e.Use(1, model("jarvis_r5", wake.KindMicroWakeWord)); err != nil {
		t.Fatalf("Use(1): %v", err)
	}

	// Peaks under the cutoff, then has fallen away for longer than the settle.
	made[wake.KindMicroWakeWord].loaded["jarvis_r5"] = 0.6
	e.score([]int16{0}, e.source)
	e.slots[1].peakAt = time.Now().Add(-2 * NearMissSettle)
	made[wake.KindMicroWakeWord].loaded["jarvis_r5"] = 0.1
	e.score([]int16{0}, e.source)

	if e.slots[1].peak != 0 {
		t.Error("the near miss was not settled, so it was never judged at all")
	}
	// OnNearMiss runs off the audio path; give a wrongly started one time to arrive.
	time.Sleep(50 * time.Millisecond)
}

// With nothing shadowed, a near miss still reaches OnNearMiss: the shadow check must not swallow the
// ordinary case.
func TestANearMissOnALiveSlotStillDucks(t *testing.T) {
	made := fakes(t)
	e := New(2, &mic.Source{})

	missed := make(chan int, 1)
	e.OnNearMiss = func(slot int, _ float64) { missed <- slot }

	if err := e.Use(0, model("jarvis_r4", wake.KindMicroWakeWord)); err != nil {
		t.Fatalf("Use(0): %v", err)
	}
	made[wake.KindMicroWakeWord].loaded["jarvis_r4"] = 0.6
	e.score([]int16{0}, e.source)
	e.slots[0].peakAt = time.Now().Add(-2 * NearMissSettle)
	made[wake.KindMicroWakeWord].loaded["jarvis_r4"] = 0.1
	e.score([]int16{0}, e.source)

	select {
	case slot := <-missed:
		if slot != 0 {
			t.Errorf("near miss on slot %d, want slot 1", slot+1)
		}
	case <-time.After(time.Second):
		t.Fatal("the live slot's near miss did not reach OnNearMiss")
	}
}
