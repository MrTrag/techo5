package recording

import (
	"os"
	"path/filepath"
	"testing"

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
	s.Opens("../escaped", 0, nil)
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
