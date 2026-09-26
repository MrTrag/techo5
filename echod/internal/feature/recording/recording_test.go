package recording

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/mic"
	"github.com/HuskerMinion/techo5/echod/internal/layout"
)

// kept points the recordings somewhere harmless and puts one turn there.
func kept(t *testing.T, id string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "recordings")
	old := layout.RecordingDir
	layout.RecordingDir = dir
	t.Cleanup(func() { layout.RecordingDir = old })

	if err := write(id, 0, make([]byte, 3200)); err != nil {
		t.Fatalf("writing a recording: %v", err)
	}
	return dir
}

// What is in these files is somebody talking in their own home, so nothing else on the device has
// any business opening them — and they are created owner-only rather than tightened afterwards,
// which would leave a moment when anything could.
func TestARecordingIsOwnerOnly(t *testing.T) {
	dir := kept(t, "4f7a1c9e2b8d0a65")

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o700 {
		t.Errorf("the recordings directory is %04o, want 0700", mode)
	}

	for _, name := range []string{"4f7a1c9e2b8d0a65.wav", "4f7a1c9e2b8d0a65.json"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if mode := fi.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s is %04o, want 0600", name, mode)
		}
	}
}

// The id in a turn_audio call is whatever the caller sent, and it names the file that is read.
func TestTurnAudioRefusesAnIDThatLeavesTheDirectory(t *testing.T) {
	kept(t, "4f7a1c9e2b8d0a65")
	s := &Store{}

	// The turn that is really there still plays, which is the point of the call.
	if _, err := s.page("4f7a1c9e2b8d0a65", 0); err != nil {
		t.Fatalf("a recording that is there did not play back: %v", err)
	}

	for _, id := range []string{
		"../../etc/passwd",
		"/etc/passwd",
		`C:\Windows\win.ini`,
		"sub/4f7a1c9e2b8d0a65",
		`sub\4f7a1c9e2b8d0a65`,
		"",
	} {
		if _, err := s.page(id, 0); err == nil {
			t.Errorf("%q was read as a turn id", id)
		}
		if s.Seconds(id) != 0 {
			t.Errorf("%q was read as a turn id by Seconds", id)
		}
	}
}

// A turn whose id could not name a file is not recorded at all, rather than written wherever the id
// points when it closes.
func TestATurnWithAnUnusableIDIsNotRecorded(t *testing.T) {
	s := &Store{}
	s.Opens("../escaped", 0, nil, mic.Loudness{})
	if s.open != "" {
		t.Errorf("a turn opened as %q", s.open)
	}
}

// A shadow clip's sidecar says what it is: the word that would have fired and the score it crossed
// at, on top of what every recording carries. A turn's sidecar carries none of that, so an old
// reader and an old file both still agree.
func TestAShadowClipIsMarkedAsOne(t *testing.T) {
	kept(t, "4f7a1c9e2b8d0a65")

	id := shadowPrefix + "0123456789abcdef"
	if err := writeWith(id, meta{Slot: 1, Shadow: true, Model: "jarvis_r5", Score: 0.91}, bytesOf(make([]int16, 1600))); err != nil {
		t.Fatalf("writing a shadow clip: %v", err)
	}

	m, err := readMeta(id)
	if err != nil {
		t.Fatalf("reading the sidecar back: %v", err)
	}
	if !m.Shadow || m.Model != "jarvis_r5" || m.Score != 0.91 || m.Slot != 1 {
		t.Errorf("sidecar is %+v, want a shadow of jarvis_r5 at 0.91 on slot 2", m)
	}
	if m.Bytes != 3200 || m.At == 0 {
		t.Errorf("sidecar has bytes=%d at=%d, want 3200 bytes and a time", m.Bytes, m.At)
	}
	if s := (&Store{}).Seconds(id); s != 0.1 {
		t.Errorf("a 1600-sample clip reports %v s, want 0.1", s)
	}

	turn, err := readMeta("4f7a1c9e2b8d0a65")
	if err != nil {
		t.Fatal(err)
	}
	if turn.Shadow || turn.Model != "" || turn.Score != 0 {
		t.Errorf("a turn's sidecar is %+v, want no shadow fields", turn)
	}
}

// A shadow clip carries the device's own score curve over its audio, rounded, and the first page of
// its audio carries the sidecar with it, so a pull over Home Assistant gets both without a shell.
// The other pages, and a turn's first page, carry no trace.
func TestAShadowClipCarriesItsTrace(t *testing.T) {
	kept(t, "4f7a1c9e2b8d0a65")

	// Both slots keep ten, so neither the clip nor the turn is pruned the moment the clip lands.
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	for slot := range 2 {
		if err := config.Set().Wake(slot).Recordings(10); err != nil {
			t.Fatal(err)
		}
	}

	s := &Store{}
	s.Shadow(1, "jarvis_r5", 0.7529, make([]int16, 1600), []float64{0.1, 0.52345, 0.7529},
		mic.Loudness{Heard: -40.04, Raw: -62.46, Floor: -71.96})

	ids, err := s.available()
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, i := range ids.IDs {
		if strings.HasPrefix(i, shadowPrefix) {
			id = i
		}
	}
	if id == "" {
		t.Fatalf("no shadow clip among %v", ids.IDs)
	}

	first, err := s.page(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Meta == nil || !first.Meta.Shadow || first.Meta.Model != "jarvis_r5" {
		t.Fatalf("page 0 carries %+v, want the shadow sidecar", first.Meta)
	}
	if got, want := first.Meta.Trace, []float64{0.1, 0.5235, 0.7529}; !slices.Equal(got, want) {
		t.Errorf("trace is %v, want %v rounded to four places", got, want)
	}
	if first.Meta.TraceMS != 20 {
		t.Errorf("trace step is %d ms, want 20", first.Meta.TraceMS)
	}
	if l := first.Meta.Loudness; l == nil || *l != (loudness{Heard: -40, Raw: -62.5, Floor: -72}) {
		t.Errorf("shadow loudness is %+v, want heard -40, raw -62.5, floor -72", l)
	}

	turn, err := s.page("4f7a1c9e2b8d0a65", 0)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Meta == nil || turn.Meta.Trace != nil || turn.Meta.Shadow {
		t.Errorf("a turn's first page carries %+v, want its plain sidecar and no trace", turn.Meta)
	}
}

// A turn keeps how loud its wake word was from the moment it opened to the sidecar it closes with,
// since that is what tells a word said in the room from one heard through a wall.
func TestATurnKeepsItsLoudness(t *testing.T) {
	kept(t, "4f7a1c9e2b8d0a65")
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	if err := config.Set().Wake(0).Recordings(10); err != nil {
		t.Fatal(err)
	}

	s := &Store{}
	s.Opens("0123456789abcdef", 0, make([]int16, 1600), mic.Loudness{Heard: -24.96, Raw: -51.04, Floor: -68.5})
	s.Frame(make([]byte, 640))
	s.Closes()

	m, err := readMeta("0123456789abcdef")
	if err != nil {
		t.Fatalf("the turn was not kept: %v", err)
	}
	if m.Loudness == nil || *m.Loudness != (loudness{Heard: -25, Raw: -51, Floor: -68.5}) {
		t.Errorf("turn loudness is %+v, want heard -25, raw -51, floor -68.5", m.Loudness)
	}
	if m.Shadow || m.Trace != nil {
		t.Errorf("a turn's sidecar is %+v, want no shadow fields", m)
	}
}

// Samples go to the WAV least significant byte first, which is what the header promises.
func TestBytesOfIsLittleEndian(t *testing.T) {
	got := bytesOf([]int16{0x0102, -1})
	want := []byte{0x02, 0x01, 0xff, 0xff}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("bytesOf gave % x, want % x", got, want)
		}
	}
}
