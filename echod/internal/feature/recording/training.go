package recording

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/mic"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
)

// Training sessions: minutes of the room, recorded on purpose, so the wake word can be trained on the
// people who live here saying it the way they actually say it.
//
// A turn only keeps the times the word worked, and a shadowed slot only the times it nearly did; the
// tries that never came close are never kept, and they are the ones a model most needs. So a session
// records everything for a set time, whoever is talking, while somebody walks the house saying the
// word near and far. Home Assistant starts one on every device at once, so each word lands on all of
// them; which device heard it loudest says which room it was said in, and the others hold the same
// word as it carries through the walls.
//
// While a session runs the wake words are still scored and logged but nothing acts on them
// (Training): the person is saying the word over and over on purpose, and a turn opening on each one
// would talk over them.

const (
	// TrainingDefault is how long a session runs when nobody says; TrainingLongest is the most it can
	// be asked for. The gain readings kept beside the audio grow with the length and travel with the
	// first page of the audio, which a message caps, so the ceiling is minutes rather than hours.
	TrainingDefault  = 3 * time.Minute
	TrainingLongest  = 5 * time.Minute
	trainingShortest = 10 * time.Second

	// KeepTraining is how many sessions a device holds. A three-minute session is under 6 MB, so this
	// is about 170 MB at most, and sessions are fetched off the device the same day.
	KeepTraining = 30

	// trainingPrefix leads a session's id, followed by who it is and the random part, so a listing
	// says what a file is and whose voice is in it without opening anything.
	trainingPrefix = "training-"

	// gainEvery is how often the leveler's gain is noted beside the audio. The audio is what the wake
	// models hear, after leveling; the gain is what turns a word's level back into how loud it reached
	// the microphones, which is what tells near from far. The gain falls over half a second and rises
	// over five, so a reading every ten frames follows it closely.
	gainEvery = 10

	// trainingLeadIn is dropped from the front, so the starting tone, which the array hears better than
	// anyone in the room, is not the first thing in the recording.
	trainingLeadIn = 600 * time.Millisecond
)

// The start is two short notes and a longer higher one, the end the same falling; neither is anything
// else the device plays, and both carry across a room.
var (
	toneTrainingStart = []speaker.Note{{Freq: 784, Ms: 90}, {Ms: 50}, {Freq: 784, Ms: 90}, {Ms: 50}, {Freq: 1047, Ms: 280}}
	toneTrainingEnd   = []speaker.Note{{Freq: 1047, Ms: 90}, {Ms: 50}, {Freq: 1047, Ms: 90}, {Ms: 50}, {Freq: 784, Ms: 280}}
)

// session is the one running, if any.
type session struct {
	who    string
	cancel context.CancelFunc
}

var (
	trainMu sync.Mutex
	running *session
)

// Training reports whether a session is recording, which is when a detected wake word opens nothing.
func (s *Store) Training() bool {
	trainMu.Lock()
	defer trainMu.Unlock()
	return running != nil
}

// TrainingLabel is who a session is for, reduced to lower-case letters: it goes into a file name.
// Anything else, or nothing, is "unknown" rather than a refusal, since the recording is worth having
// either way.
func TrainingLabel(who string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(who)) {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
		if b.Len() == 16 {
			break
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// TrainingLength is how long a session asked for in seconds should run: the default for nothing, and
// held between the shortest and the longest.
func TrainingLength(seconds int) time.Duration {
	if seconds <= 0 {
		return TrainingDefault
	}
	return min(max(time.Duration(seconds)*time.Second, trainingShortest), TrainingLongest)
}

// StartTraining begins a session for who, lasting seconds (see TrainingLength). A session already
// running is left alone: every device is started at once and a second press is somebody being sure.
func (s *Store) StartTraining(who string, seconds int) {
	who, length := TrainingLabel(who), TrainingLength(seconds)

	trainMu.Lock()
	if running != nil {
		already := running.who
		trainMu.Unlock()
		slog.Info("training session already running", "who", already)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), length+trainingLeadIn)
	running = &session{who: who, cancel: cancel}
	trainMu.Unlock()

	slog.Info("training session started", "who", who, "seconds", length.Seconds())
	go func() {
		defer func() {
			trainMu.Lock()
			running = nil
			trainMu.Unlock()
			cancel()
		}()

		speaker.Sound().Chime(toneTrainingStart)
		frames, stop := mic.Get().Listen("training")
		began := time.Now()
		audio, gains := collect(ctx, frames, trainingLeadIn, levelerGain)
		stop()
		speaker.Sound().Chime(toneTrainingEnd)

		s.keepTraining(who, began, audio, gains)
	}()
}

// StopTraining ends the running session early. What was recorded is kept.
func (s *Store) StopTraining() {
	trainMu.Lock()
	defer trainMu.Unlock()
	if running != nil {
		slog.Info("training session stopped early", "who", running.who)
		running.cancel()
	}
}

// levelerGain is the gain the leveler has on right now in dB, and nothing when leveling is off.
func levelerGain() float64 {
	src := mic.Get()
	if !src.Leveling() {
		return 0
	}
	return src.Gain()
}

// collect gathers frames until ctx ends or the frames stop, dropping leadIn from the front, and notes
// gain() every gainEvery frames it keeps, the first with the first frame.
func collect(ctx context.Context, frames <-chan []int16, leadIn time.Duration, gain func() float64) ([]int16, []float64) {
	skip := int(leadIn / (time.Second / (mic.Rate / mic.FrameSamples)))
	var audio []int16
	var gains []float64
	kept := 0
	for {
		select {
		case <-ctx.Done():
			return audio, gains
		case f, ok := <-frames:
			if !ok {
				return audio, gains
			}
			if skip > 0 {
				skip--
				continue
			}
			if kept%gainEvery == 0 {
				gains = append(gains, math.Round(gain()*10)/10)
			}
			audio = append(audio, f...)
			kept++
		}
	}
}

// keepTraining writes a session beside the turns, marked as one in its sidecar with who it was for and
// the gain readings, then prunes.
func (s *Store) keepTraining(who string, began time.Time, audio []int16, gains []float64) {
	if len(audio) == 0 {
		slog.Warn("training session heard nothing, nothing kept", "who", who)
		return
	}
	id := trainingPrefix + who + "-" + randomID()

	s.disk.Lock()
	defer s.disk.Unlock()

	m := meta{
		Training: who,
		Started:  began.Unix(),
		Gain:     gains,
		GainMS:   gainEvery * mic.FrameSamples * 1000 / mic.Rate,
	}
	if err := writeWith(id, m, bytesOf(audio)); err != nil {
		slog.Error("saving a training session failed", "id", id, "err", err)
		return
	}
	slog.Info("training session kept", "id", id, "who", who,
		"seconds", fmt.Sprintf("%.1f", float64(len(audio))/float64(mic.Rate)))
	s.prune()
}
