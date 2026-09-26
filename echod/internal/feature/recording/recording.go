// Package recording keeps the last few turns as audio on disk, and serves them to Home Assistant.
//
// What is kept is exactly what was sent to be transcribed, so listening back answers the question a
// transcript raises: whether the device mis-heard, or heard correctly and the pipeline did the rest.
//
// Each kept turn is two files named by its id: the WAV, and a metadata sidecar. A turn is complete
// only once both are written, which is what lets pruning tell a finished recording from one a crash
// left half-written.
package recording

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/HuskerMinion/techo5/echod/internal/component"
	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/mic"
	"github.com/HuskerMinion/techo5/echod/internal/layout"
	"github.com/HuskerMinion/techo5/echod/internal/lib/safename"
)

// Slots is how many assistants there are to keep recordings for. Named here rather than taken from the
// wake feature, which serves the turns this records and cannot depend on it back.
const Slots = 2

func init() {
	component.Register(component.Device, Get(), component.Order(20))
}

// Page is how many raw bytes go in one answer. The transport caps a message at 65515 bytes and base64
// costs a third again, so 32 KiB encodes to about 43.7 KiB and leaves room for the rest of the reply.
const Page = 32 * 1024

// Longest is how much of one turn is kept. A request is seconds long; a microphone left open by a
// pipeline that never closed the run is not, and the memory holding a turn until it closes is not
// there for that.
const Longest = 30 * mic.Rate * 2

// KeepMost is the most an assistant can be set to hold. A fresh device keeps none: recording what
// someone said is off until they turn it on.
const KeepMost = 10

// Prewake is how much of the microphone's history opens every recording, whatever the turn sends on
// to the pipeline. A wake word can only be recognized once it has been spoken, and detection fires
// about half a second after it ends, so a recording that began where the stream did held the tail
// of the request and never the word: what a kept turn is worth as training material, and what a
// false wake can be replayed to find out about, is in these two seconds. It is the whole of what
// the microphone keeps (mic/history.go), 64 KB a turn.
const Prewake = 2 * time.Second

const (
	wavExt  = ".wav"
	metaExt = ".json"
)

// meta is what is stored beside a recording: enough to prune it and to report its length after a
// restart, without re-reading the audio.
type meta struct {
	Slot  int   `json:"slot"`
	Bytes int   `json:"bytes"`
	At    int64 `json:"at"`

	// A shadow clip is a detection that was watched and not acted on (detect.Engine.Shadowed): the
	// wake word that would have fired and the score it crossed at, so the clip can be told from a
	// turn and judged against what the live word did with the same sound. Absent on a turn.
	Shadow bool    `json:"shadow,omitempty"`
	Model  string  `json:"model,omitempty"`
	Score  float64 `json:"score,omitempty"`

	// Trace is the device's own score over the clip, one per TraceMS of audio, the last of them the
	// score that crossed on the clip's last frame. It is what the desk scorer's curve over the same
	// file is compared against. Absent on a turn.
	Trace   []float64 `json:"trace,omitempty"`
	TraceMS int       `json:"trace_ms,omitempty"`

	// Loudness is how loud the wake word was at the moment it was detected, on a turn and on a shadow
	// clip alike: what tells a word said in this room from one heard through a wall. Absent on
	// recordings made before it was kept.
	Loudness *loudness `json:"loudness,omitempty"`
}

// loudness is mic.Loudness as the sidecar stores it, in dBFS to a tenth.
type loudness struct {
	Heard float64 `json:"heard_db"`
	Raw   float64 `json:"raw_db"`
	Floor float64 `json:"floor_db"`
}

func loudnessOf(l mic.Loudness) *loudness {
	r := func(v float64) float64 { return math.Round(v*10) / 10 }
	return &loudness{Heard: r(l.Heard), Raw: r(l.Raw), Floor: r(l.Floor)}
}

type Store struct {
	// mu guards the turn being recorded. open is its id, empty between turns; buf gathers its audio.
	mu   sync.Mutex
	open string
	slot int
	buf  []byte
	loud mic.Loudness

	// disk serializes writing a recording against pruning, so a sweep cannot delete a turn in the
	// instant between its two files being written.
	disk sync.Mutex

	// One per assistant, because how much of an assistant is worth keeping is about what it is for.
	keep []*esphome.Number
}

var (
	once   sync.Once
	shared *Store
)

func Get() *Store {
	once.Do(func() {
		shared = &Store{}

		for n := range Slots {
			slot := n
			number := &esphome.Number{
				Base: esphome.Base{
					ObjectID: fmt.Sprintf("keep_recordings_%d", slot+1),
					Name:     "Recordings kept",
					Icon:     "mdi:record-rec",
					DeviceID: component.AssistantDevice(slot),
					Category: esphome.CategoryConfig,
				},
				Min: 0, Max: KeepMost, Step: 1,
				Mode: esphome.NumberBox,
			}

			number.OnCommand = func(v float32) {
				number.Set(v)
				if err := config.Set().Wake(slot).Recordings(int(v)); err != nil {
					slog.Error("saving how many recordings to keep failed", "slot", slot+1, "err", err)
				}
				shared.Prune()
			}
			shared.keep = append(shared.keep, number)
		}
	})
	return shared
}

func (s *Store) Name() string { return "recording" }

func (s *Store) Entities() []esphome.Entity {
	out := make([]esphome.Entity, 0, len(s.keep))
	for _, number := range s.keep {
		out = append(out, number)
	}
	return out
}

func (s *Store) Restore(c config.Config) {
	for slot, number := range s.keep {
		count := 0
		if slot < len(c.Wake.Words) {
			count = c.Wake.Words[slot].Recordings
		}
		number.Set(float32(count))
	}
	s.Prune()
}

// Actions is how Home Assistant reaches a recording: recordings lists what is held, turn_audio fetches
// one, paged because a recording is far larger than a message.
func (s *Store) Actions() []*esphome.Action {
	return []*esphome.Action{
		{
			Name:    "recordings",
			Answers: true,
			Run: func(esphome.Call) (any, error) {
				return s.available()
			},
		},
		{
			Name:    "turn_audio",
			Args:    []esphome.Arg{{Name: "id", Type: esphome.ArgString}, {Name: "page", Type: esphome.ArgInt}},
			Answers: true,
			Run: func(c esphome.Call) (any, error) {
				return s.page(c.String("id"), c.Int("page"))
			},
		},
	}
}

// Available is the ids the device still holds audio for, so the card offers to play only those rather
// than every turn that once had a recording. A turn's event says it had audio for as long as the event
// lives; the file behind it does not.
type Available struct {
	Version int      `json:"version"`
	IDs     []string `json:"ids"`
}

func (s *Store) available() (*Available, error) {
	out := &Available{Version: 1, IDs: []string{}}

	entries, err := os.ReadDir(layout.RecordingDir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}

	// Keyed off the metadata, so a WAV still being written or left by a crash is not offered as ready.
	for _, e := range entries {
		if name := e.Name(); strings.HasSuffix(name, metaExt) {
			out.IDs = append(out.IDs, strings.TrimSuffix(name, metaExt))
		}
	}
	return out, nil
}

// Opens starts recording a turn, beginning with history: the microphone's last second as it was at
// the moment of the wake, which holds the word itself (see Prewake). loud is how loud the word in it
// was, kept in the sidecar. An assistant set to keep none records nothing, which is how somebody turns
// it off for one of them without touching the other.
func (s *Store) Opens(id string, slot int, history []int16, loud mic.Loudness) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// A turn with no id is one nothing is recording for; any other id that cannot name a file is
	// wrong enough to say so, since the two files are named after it.
	if !safename.OK(id) {
		if id != "" {
			slog.Error("a turn was not recorded: its id cannot name a file", "id", id)
		}
		s.open, s.buf = "", nil
		return
	}
	if keeps(slot) <= 0 {
		s.open, s.buf = "", nil
		return
	}
	s.open, s.slot, s.buf, s.loud = id, slot, bytesOf(history), loud
}

// shadowPrefix leads the id of a shadow clip, so a listing tells one from a turn without opening its
// sidecar. The rest is random, as a turn's id is.
const shadowPrefix = "shadow-"

// Shadow keeps the audio of a detection that was watched rather than acted on, whole and at once:
// there is no turn to stream the rest of, so what the microphone held at the crossing is the whole of
// the clip. It counts against the slot's own Recordings, so the "Recordings kept" of the assistant
// being shadowed is how many of these stay, and an assistant set to keep none keeps none of these
// either.
func (s *Store) Shadow(slot int, model string, score float64, history []int16, trace []float64, loud mic.Loudness) {
	if keeps(slot) <= 0 || len(history) == 0 {
		return
	}
	id := shadowPrefix + randomID()

	s.disk.Lock()
	defer s.disk.Unlock()

	m := meta{Slot: slot, Shadow: true, Model: model, Score: score, Loudness: loudnessOf(loud)}
	if len(trace) > 0 {
		// Four places is all the model's output resolves to, and it keeps the sidecar small.
		m.Trace = make([]float64, len(trace))
		for i, v := range trace {
			m.Trace[i] = math.Round(v*10000) / 10000
		}
		// One score per microphone frame, which is what the engine records (detect.TraceStep).
		m.TraceMS = mic.FrameSamples * 1000 / mic.Rate
	}
	if err := writeWith(id, m, bytesOf(history)); err != nil {
		slog.Error("saving a shadow clip failed", "id", id, "err", err)
		return
	}
	slog.Info("shadow clip kept", "id", id, "slot", slot+1, "model", model, "score", score,
		"seconds", float64(len(history))/float64(mic.Rate))
	s.prune()
}

// bytesOf is samples as the little-endian bytes the WAV holds.
func bytesOf(samples []int16) []byte {
	buf := make([]byte, 0, 2*len(samples))
	for _, v := range samples {
		buf = append(buf, byte(v), byte(v>>8))
	}
	return buf
}

// randomID is sixteen hex digits from the system's randomness, as a turn's id is, and the clock if
// that is somehow unavailable.
func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// Frame takes audio for whichever turn is open. Called from the streamer, which does not know or care
// whether anything is being kept.
func (s *Store) Frame(pcm []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.open == "" || len(s.buf) >= Longest {
		return
	}
	s.buf = append(s.buf, pcm...)
}

// Closes stops recording and writes what was gathered. A turn that gathered nothing is dropped rather
// than saved as an empty file somebody could press play on.
func (s *Store) Closes() {
	s.mu.Lock()
	id, slot, buf, loud := s.open, s.slot, s.buf, s.loud
	s.open, s.buf = "", nil
	s.mu.Unlock()

	if id == "" || len(buf) == 0 {
		return
	}

	s.disk.Lock()
	defer s.disk.Unlock()

	if err := writeWith(id, meta{Slot: slot, Loudness: loudnessOf(loud)}, buf); err != nil {
		slog.Error("saving a recording failed", "id", id, "err", err)
		return
	}
	s.prune()
}

// Seconds is how long a turn's recording runs, and zero when there is none, which is what a turn
// reports so nothing offers to play what was never kept. Read from the sidecar so it is still right
// for a recording that outlived the process that made it.
func (s *Store) Seconds(id string) float64 {
	m, err := readMeta(id)
	if err != nil {
		return 0
	}
	return float64(m.Bytes) / float64(mic.Rate*2)
}

// Answer is one page of a recording, as the frontend expects it. Version is the contract the reader
// checks: an answer without it is rejected, so it is not optional.
type Answer struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Page    int    `json:"page"`
	Pages   int    `json:"pages"`
	MIME    string `json:"mime"`
	Data    string `json:"data"`

	// Meta is the recording's sidecar, sent with the first page only, so whatever pulls the audio
	// off the device gets what the device knew about it (a shadow clip's word, score and trace)
	// without a shell. A reader that only wants the audio ignores it.
	Meta *meta `json:"meta,omitempty"`
}

// page reads the WAV and hands back one slice of it. The whole file is already a WAV, so the first
// page opens as audio on its own and the rest append to it.
//
// The id comes in over the wire, in the call Home Assistant's card makes to play a turn back, so it
// is whatever the caller cared to send: it names a file and it is checked before it does.
func (s *Store) page(id string, page int) (*Answer, error) {
	if !safename.OK(id) {
		return nil, fmt.Errorf("%q is not a turn id", id)
	}

	whole, err := os.ReadFile(wavPath(id))
	if err != nil {
		return nil, fmt.Errorf("no recording for turn %s", id)
	}

	pages := (len(whole) + Page - 1) / Page
	if page < 0 || page >= pages {
		return nil, fmt.Errorf("page %d of %d", page, pages)
	}

	end := min((page+1)*Page, len(whole))
	a := &Answer{
		Version: 1,
		ID:      id,
		Page:    page,
		Pages:   pages,
		MIME:    "audio/wav",
		Data:    base64.StdEncoding.EncodeToString(whole[page*Page : end]),
	}
	// The sidecar is optional here: a recording whose sidecar is missing or unreadable is still
	// audio worth having.
	if page == 0 {
		if m, err := readMeta(id); err == nil {
			a.Meta = &m
		}
	}
	return a, nil
}

// Prune keeps the newest few for each assistant and clears away the rest, so a slot set lower, a
// recording left by a restart, or a half-written file a crash left behind does not linger.
func (s *Store) Prune() {
	s.disk.Lock()
	defer s.disk.Unlock()
	s.prune()
}

// prune assumes the disk lock is held.
func (s *Store) prune() {
	entries, err := os.ReadDir(layout.RecordingDir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		slog.Error("reading the recordings failed", "dir", layout.RecordingDir, "err", err)
		return
	}

	type rec struct {
		id string
		at int64
	}
	bySlot := map[int][]rec{}
	complete := map[string]bool{}
	wavs := []string{}

	for _, e := range entries {
		switch name := e.Name(); {
		case strings.HasSuffix(name, metaExt):
			id := strings.TrimSuffix(name, metaExt)
			m, err := readMeta(id)
			if err != nil {
				continue
			}
			complete[id] = true
			bySlot[m.Slot] = append(bySlot[m.Slot], rec{id, m.At})
		case strings.HasSuffix(name, wavExt):
			wavs = append(wavs, strings.TrimSuffix(name, wavExt))
		}
	}

	for slot, recs := range bySlot {
		sort.Slice(recs, func(i, j int) bool { return recs[i].at > recs[j].at })
		for i, r := range recs {
			if i >= keeps(slot) {
				remove(r.id)
			}
		}
	}

	// A WAV without its sidecar is a turn a crash cut short. Pruning runs minutes apart while a close
	// writes both back to back, so this only ever catches a genuine leftover.
	for _, id := range wavs {
		if !complete[id] {
			remove(id)
		}
	}
}

// keeps is how many recordings an assistant is set to hold.
func keeps(slot int) int {
	words := config.Get().Wake.Words
	if slot < 0 || slot >= len(words) {
		return 0
	}
	return max(words[slot].Recordings, 0)
}

// The modes below are owner-only, and are the modes the files are created with rather than modes
// something tightens them to afterwards: between the two there is a moment when anything on the
// device can open them. What is in them is the household talking in its own rooms, which nothing
// else running here has any business reading.
const (
	dirMode  = 0o700
	fileMode = 0o600
)

func write(id string, slot int, pcm []byte) error {
	return writeWith(id, meta{Slot: slot}, pcm)
}

// writeWith is write with the rest of the sidecar chosen by the caller. The size and the time are
// filled in here, since they are what the audio and the clock say rather than anything to choose.
func writeWith(id string, m meta, pcm []byte) error {
	if err := os.MkdirAll(layout.RecordingDir, dirMode); err != nil {
		return err
	}
	// MkdirAll leaves a folder that is already there as it is, and older builds made it open to all.
	if err := os.Chmod(layout.RecordingDir, dirMode); err != nil {
		return err
	}
	if err := os.WriteFile(wavPath(id), wav(pcm), fileMode); err != nil {
		return err
	}

	m.Bytes, m.At = len(pcm), time.Now().Unix()
	blob, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath(id), blob, fileMode)
}

func remove(id string) {
	for _, path := range []string{wavPath(id), metaPath(id)} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Error("removing a recording failed", "path", path, "err", err)
		}
	}
}

// readMeta reads a turn's sidecar. Ids reach this from a Home Assistant call as well as from the
// directory listing, so the same check stands in front of the read.
func readMeta(id string) (meta, error) {
	if !safename.OK(id) {
		return meta{}, fmt.Errorf("%q is not a turn id", id)
	}

	blob, err := os.ReadFile(metaPath(id))
	if err != nil {
		return meta{}, err
	}
	var m meta
	return m, json.Unmarshal(blob, &m)
}

func wavPath(id string) string  { return filepath.Join(layout.RecordingDir, id+wavExt) }
func metaPath(id string) string { return filepath.Join(layout.RecordingDir, id+metaExt) }

// wav is a 16-bit mono header for what the microphone gives, which is the only format kept.
func wav(pcm []byte) []byte {
	const header = 44
	out := make([]byte, 0, header+len(pcm))

	put32 := func(v uint32) {
		out = append(out, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	}
	put16 := func(v uint16) { out = append(out, byte(v), byte(v>>8)) }

	out = append(out, "RIFF"...)
	put32(uint32(header - 8 + len(pcm)))
	out = append(out, "WAVEfmt "...)
	put32(16)
	put16(1)
	put16(1)
	put32(mic.Rate)
	put32(mic.Rate * 2)
	put16(2)
	put16(16)
	out = append(out, "data"...)
	put32(uint32(len(pcm)))

	return append(out, pcm...)
}
