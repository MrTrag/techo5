package detect

import (
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/mic"
	"github.com/HuskerMinion/techo5/echod/internal/lib/wake"
)

// A word is ignored as said in another room only when the gate is on AND the word was measured AND it
// came in under the floor. Measured on 26 September 2026: in-room words -43 to -50 dBFS raw, other
// rooms -60 and -63; the floor sits at -55 between them.
func TestOtherRoomIsOnlyAQuietMeasuredWordWithTheGateOn(t *testing.T) {
	for _, c := range []struct {
		name string
		on   bool
		raw  float64
		want bool
	}{
		{"in the room, gate on", true, -47, false},
		{"another room, gate on", true, -61, true},
		{"another room, gate off", false, -61, false},
		{"exactly on the floor is answered", true, -55, false},
		{"nothing measured is answered", true, silentDB, false},
	} {
		if got := otherRoom(c.on, -55, mic.Loudness{Raw: c.raw}); got != c.want {
			t.Errorf("%s: otherRoom = %v, want %v", c.name, got, c.want)
		}
	}
}

// The floor defaults to -56 dBFS until one is set, and a set one is used as it is.
func TestTheGateDefaultsToTheMeasuredFloor(t *testing.T) {
	if got := (config.Gate{}).Level(); got != -56 {
		t.Errorf("unset floor is %v, want -56", got)
	}
	if got := (config.Gate{DB: -58.5}).Level(); got != -58.5 {
		t.Errorf("set floor is %v, want -58.5", got)
	}
}

// With the gate on but nothing measured (an engine with no microphone history, as on a fresh start),
// a live detection still fires: the gate never drops a word it could not measure.
func TestTheGateNeverDropsAnUnmeasuredWord(t *testing.T) {
	made := fakes(t)
	e := New(1, &mic.Source{})
	e.Gate = func(int) (bool, float64) { return true, -55 }

	fired := make(chan int, 1)
	e.OnDetect = func(slot int) { fired <- slot }
	if err := e.Use(0, model("jarvis_r6", wake.KindMicroWakeWord)); err != nil {
		t.Fatalf("Use(0): %v", err)
	}
	made[wake.KindMicroWakeWord].loaded["jarvis_r6"] = 0.95
	e.score([]int16{0}, e.source)

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("an unmeasured word was dropped by the gate")
	}
	if e.slots[0].ignored {
		t.Error("the hold is marked ignored for a word that fired")
	}
}
