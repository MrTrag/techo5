package recording

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/mic"
)

// Who a session is for goes into a file name, so it is cut down to letters, and nothing usable is
// "unknown" rather than a refusal.
func TestTrainingLabel(t *testing.T) {
	for in, want := range map[string]string{
		"Steve":                     "steve",
		" Deb ":                     "deb",
		"":                          "unknown",
		"../../etc":                 "etc",
		"42":                        "unknown",
		"averyveryverylongnamehere": "averyveryverylon",
	} {
		if got := TrainingLabel(in); got != want {
			t.Errorf("TrainingLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// Nothing asked for is the three-minute default, and anything else is held between ten seconds and
// five minutes.
func TestTrainingLength(t *testing.T) {
	for in, want := range map[int]time.Duration{
		0:     3 * time.Minute,
		-5:    3 * time.Minute,
		3:     10 * time.Second,
		180:   3 * time.Minute,
		90:    90 * time.Second,
		10000: 5 * time.Minute,
	} {
		if got := TrainingLength(in); got != want {
			t.Errorf("TrainingLength(%d) = %v, want %v", in, got, want)
		}
	}
}

// A session drops the lead-in (the starting tone), keeps every frame after it, and notes the gain
// with the first kept frame and every gainEvery after; it ends when the frames do.
func TestCollectDropsTheLeadInAndNotesTheGain(t *testing.T) {
	frames := make(chan []int16, 64)
	lead := 600 * time.Millisecond
	skip := int(lead / (20 * time.Millisecond))
	for range skip {
		frames <- make([]int16, mic.FrameSamples) // the tone, dropped
	}
	for range 25 {
		f := make([]int16, mic.FrameSamples)
		for i := range f {
			f[i] = 7
		}
		frames <- f
	}
	close(frames)

	audio, gains := collect(context.Background(), frames, lead, func() float64 { return 12.34 })
	if len(audio) != 25*mic.FrameSamples {
		t.Fatalf("kept %d samples, want %d: the lead-in is %d frames", len(audio), 25*mic.FrameSamples, skip)
	}
	for _, v := range audio {
		if v != 7 {
			t.Fatal("a lead-in frame was kept")
		}
	}
	if len(gains) != 3 || gains[0] != 12.3 {
		t.Errorf("gains %v, want three readings of 12.3 for 25 frames", gains)
	}
}

// Stopping early ends the session with what it has.
func TestCollectEndsWhenStopped(t *testing.T) {
	frames := make(chan []int16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		collect(ctx, frames, 0, func() float64 { return 0 })
		close(done)
	}()
	frames <- make([]int16, mic.FrameSamples)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the session did not end when stopped")
	}
}

// A session is kept apart from the turns: it is not counted against an assistant's recordings, so
// pruning the turns never takes it, it has its own limit, and its sidecar says whose it is.
func TestATrainingSessionIsKeptApart(t *testing.T) {
	dir := kept(t, "4f7a1c9e2b8d0a65")
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	if err := config.Set().Wake(0).Recordings(1); err != nil {
		t.Fatal(err)
	}

	s := &Store{}
	s.keepTraining("steve", time.Unix(1790420000, 0), make([]int16, 3200), []float64{20, 21.5})
	// A second turn on slot 1, which keeps one: the older turn goes, the session must not.
	time.Sleep(1100 * time.Millisecond) // sidecar times are whole seconds
	if err := write("0123456789abcdef", 0, make([]byte, 640)); err != nil {
		t.Fatal(err)
	}
	s.Prune()

	ids, err := s.available()
	if err != nil {
		t.Fatal(err)
	}
	var session string
	for _, id := range ids.IDs {
		if strings.HasPrefix(id, trainingPrefix+"steve-") {
			session = id
		}
	}
	if session == "" {
		t.Fatalf("the session was pruned with the turns: %v", ids.IDs)
	}
	if len(ids.IDs) != 2 {
		t.Errorf("held %v, want the session and the newest turn", ids.IDs)
	}

	first, err := s.page(session, 0)
	if err != nil {
		t.Fatal(err)
	}
	m := first.Meta
	if m == nil || m.Training != "steve" || m.Started != 1790420000 || len(m.Gain) != 2 || m.GainMS != 200 {
		t.Errorf("session sidecar %+v, want steve, the start time, two gains every 200 ms", m)
	}

	// Its own limit: more than KeepTraining sessions and the oldest go.
	for i := range KeepTraining + 2 {
		m := meta{Training: "deb", Started: int64(i)}
		if err := writeWith(trainingPrefix+"deb-"+randomID(), m, make([]byte, 64)); err != nil {
			t.Fatal(err)
		}
	}
	s.Prune()
	n := 0
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), trainingPrefix) && strings.HasSuffix(e.Name(), metaExt) {
			n++
		}
	}
	if n != KeepTraining {
		t.Errorf("%d sessions held, want %d", n, KeepTraining)
	}
}
