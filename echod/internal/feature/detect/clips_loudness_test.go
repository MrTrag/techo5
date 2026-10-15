package detect

// jarvis: kept wake clips carry how loud the word was, as a shadow clip's and a turn's sidecar do. A file
// of its own, so upstream's clips_test.go merges untouched.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/mic"
)

func TestAClipCarriesHowLoudTheWordWas(t *testing.T) {
	c, saved := testClips(t, true)
	c.loudness = func() mic.Loudness { return mic.Loudness{Heard: -30.04, Raw: -54.26, Floor: -66.2} }
	c.frame([]int16{1, 2})
	c.nearMiss(0, 0.8)
	c.scored(0, 0.99, 0.98)
	c.settle()
	if len(*saved) != 2 || (*saved)[0].loud == nil || (*saved)[1].loud == nil || (*saved)[1].loud.Raw != -54.26 {
		t.Fatalf("the near miss and the wake should both carry the loudness: %+v", *saved)
	}

	c.write(clip{kind: "nearmiss", at: time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC), peak: 0.8, cutoff: 0.98,
		pcm: []int16{1}, loud: (*saved)[0].loud})
	meta, err := os.ReadFile(filepath.Join(c.dir, "nearmiss", "20261015-100000-000-s1-p0.80-c0.98.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"kind":"nearmiss","at":"2026-10-15T10:00:00Z","slot":1,"model":"","score":0.8,"cutoff":0.98,` +
		`"loudness":{"heard_db":-30,"raw_db":-54.3,"floor_db":-66.2}}`
	if got := string(meta); got != want {
		t.Fatalf("the sidecar:\n got %s\nwant %s", got, want)
	}
}

// Nothing to measure from, nothing is claimed: the sidecar is upstream's exactly.
func TestAClipWithNoLoudnessSaysNothingOfIt(t *testing.T) {
	c, saved := testClips(t, true)
	c.frame([]int16{1, 2})
	c.nearMiss(0, 0.8)
	if len(*saved) != 1 || (*saved)[0].loud != nil {
		t.Fatalf("no loudness to measure, yet: %+v", *saved)
	}
}
