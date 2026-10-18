package wakeword

import (
	"bytes"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
)

// ownSounds points the speaker at a folder of the test's own, holding wake_<name>.wav for each name.
func ownSounds(t *testing.T, names ...string) {
	t.Helper()
	was := speaker.SoundsDir
	speaker.SoundsDir = t.TempDir()
	t.Cleanup(func() { speaker.SoundsDir = was })
	for _, name := range names {
		frames := speaker.Rate / 4 // 250 ms
		var b bytes.Buffer
		b.WriteString("RIFF")
		binary.Write(&b, binary.LittleEndian, uint32(36+frames*2))
		b.WriteString("WAVEfmt ")
		binary.Write(&b, binary.LittleEndian, uint32(16))
		binary.Write(&b, binary.LittleEndian, uint16(1))
		binary.Write(&b, binary.LittleEndian, uint16(1))
		binary.Write(&b, binary.LittleEndian, uint32(speaker.Rate))
		binary.Write(&b, binary.LittleEndian, uint32(speaker.Rate*2))
		binary.Write(&b, binary.LittleEndian, uint16(2))
		binary.Write(&b, binary.LittleEndian, uint16(16))
		b.WriteString("data")
		binary.Write(&b, binary.LittleEndian, uint32(frames*2))
		for range frames {
			binary.Write(&b, binary.LittleEndian, int16(8000))
		}
		if err := os.WriteFile(filepath.Join(speaker.SoundsDir, "wake_"+name+".wav"), b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// answering is a voice-ID service that always names who, after delay, and counts what it was asked.
func answering(t *testing.T, who string, delay time.Duration) (url string, asked *atomic.Int32, got *atomic.Int32) {
	t.Helper()
	asked, got = new(atomic.Int32), new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		body, _ := readAll(r)
		got.Store(int32(len(body)))
		if r.Header.Get("Content-Type") != "audio/L16; rate=16000" {
			http.Error(w, "not audio", http.StatusBadRequest)
			return
		}
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"speaker": "` + who + `", "scores": {"Steve": 0.6, "Deb": 0.1}, "check_ms": 12}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/wake", asked, got
}

func readAll(r *http.Request) ([]byte, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(r.Body)
	return b.Bytes(), err
}

// The service names Steve and there is a recording for him: the wake plays his sound, and the audio
// went over as 16-bit samples.
func TestWhoNamesTheSpeakerWithARecording(t *testing.T) {
	ownSounds(t, "steve")
	url, asked, got := answering(t, "Steve", 0)
	history := make([]int16, 32000)
	if name := who(url, time.Second, history); name != "steve" {
		t.Fatalf("named %q, want steve", name)
	}
	if asked.Load() != 1 || got.Load() != 64000 {
		t.Errorf("asked %d times with %d bytes, want once with 64000", asked.Load(), got.Load())
	}
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	if d := ChimeLengthNamed(0, false, "steve"); d <= 0 || d > 250*time.Millisecond {
		t.Errorf("the named sound's length is %v", d)
	}
}

// A name with no recording of its own is no name: the ordinary wake sound plays.
func TestANameWithoutARecordingIsNobody(t *testing.T) {
	ownSounds(t, "steve")
	url, _, _ := answering(t, "Deb", 0)
	if name := who(url, time.Second, make([]int16, 16000)); name != "" {
		t.Fatalf("named %q, want nobody", name)
	}
	if name := who(url, time.Second, nil); name != "" {
		t.Fatalf("named %q with no audio", name)
	}
}

// An answer that comes too late is no answer, and the wake does not wait for it.
func TestASlowAnswerIsNobody(t *testing.T) {
	ownSounds(t, "steve")
	url, _, _ := answering(t, "Steve", 400*time.Millisecond)
	t0 := time.Now()
	name := who(url, 100*time.Millisecond, make([]int16, 16000))
	if took := time.Since(t0); name != "" || took > 300*time.Millisecond {
		t.Fatalf("named %q after %v, want nobody within the wait", name, took)
	}
}

// With no named sound to play, nobody is asked at all.
func TestNobodyIsAskedWithoutNamedSounds(t *testing.T) {
	ownSounds(t)
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	t0 := time.Now()
	if name := Who(0, make([]int16, 16000)); name != "" {
		t.Fatalf("named %q with no sounds", name)
	}
	if took := time.Since(t0); took > 50*time.Millisecond {
		t.Errorf("took %v: something was asked", took)
	}
}

// A question put at the word is answered by the time the turn opens: the answer is collected at once,
// the service was asked only once, and the answer is collected only once.
func TestAQuestionPutAtTheWordIsAnsweredByTheTurn(t *testing.T) {
	ownSounds(t, "steve")
	url, asked, _ := answering(t, "Steve", 50*time.Millisecond)
	ask(0, url, time.Second, make([]int16, 32000))
	time.Sleep(150 * time.Millisecond) // the nearest window
	t0 := time.Now()
	name, put := collect(0, time.Second)
	if took := time.Since(t0); !put || name != "steve" || took > 20*time.Millisecond {
		t.Fatalf("collected %q (put %v) after %v, want steve at once", name, put, took)
	}
	if asked.Load() != 1 {
		t.Errorf("asked %d times, want once", asked.Load())
	}
	if _, put := collect(0, time.Second); put {
		t.Error("the answer was collected twice")
	}
}

// With no question put, or one for another slot, Who asks for itself.
func TestWhoAsksWhenNothingWasPut(t *testing.T) {
	ownSounds(t, "steve")
	if _, put := collect(0, time.Second); put {
		t.Fatal("an answer with no question")
	}
	url, _, _ := answering(t, "Steve", 0)
	ask(1, url, time.Second, make([]int16, 16000))
	if _, put := collect(0, time.Second); put {
		t.Fatal("another slot's answer was taken")
	}
}
