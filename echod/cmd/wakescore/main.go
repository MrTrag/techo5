// Command wakescore replays WAV files through the wake word engines exactly as the Dot runs them and
// reports what the Dot would have done with each one: every detection, every near miss, and the peak
// each model reached. The numbers are the device's numbers because the code is the device's code:
// the same microWakeWord and openWakeWord engines, fed the same 20 ms frames, judged by the same
// threshold, hold and refractory as internal/feature/detect.
//
// Models come from a directory laid out like the Dot's own: one .tflite per wake word with an
// optional .json beside it. Which engine runs each one is read from the model, as on the device.
//
//	wakescore -models DIR [-wake id,id] [-threshold 0.7] [-trace DIR] [-v] FILE|DIR...
//
// Every file is scored from cold by default, with silence either side, so one file cannot prime the
// next; -fresh=false plays them back to back as one stream, which is what a long recording wants.
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zserge/microwakeword"

	"github.com/HuskerMinion/techo5/echod/internal/lib/mwwfront"
	"github.com/HuskerMinion/techo5/echod/internal/lib/oww"
	"github.com/HuskerMinion/techo5/echod/internal/lib/wake"
)

// The Dot hands its listeners 20 ms frames at 16 kHz (mic.FrameSamples). The mic package is device
// code, so the numbers are repeated here rather than imported.
const (
	rate         = 16000
	frameSamples = rate / 50
	frameTime    = 20 * time.Millisecond
)

// The judge's timings, copied from internal/feature/detect (Hold, Refractory, NearMiss,
// NearMissSettle). Importing that package drags the microphone and device hardware in with it, which
// has nothing to score on a PC; keep these in step with it by hand.
const (
	hold           = 300 * time.Millisecond
	refractory     = 800 * time.Millisecond
	nearMiss       = 0.5
	nearMissSettle = 700 * time.Millisecond
)

// neverFires is what the daemon hands microWakeWord so the detector's own cutoff never triggers; the
// judge applies the threshold, as on the device.
const neverFires = 1.1

func main() {
	modelDir := flag.String("models", "", "directory of wake word models, laid out like the Dot's (.tflite, optional .json beside it)")
	wakeIDs := flag.String("wake", "", "comma-separated model ids to score (default: every model in -models)")
	threshold := flag.Float64("threshold", 0.70, "detection threshold, as set in Home Assistant")
	overrides := flag.String("thresholds", "", "per-model thresholds, id=0.7,id2=0.5")
	fresh := flag.Bool("fresh", true, "score every file from cold engines with silence either side; false plays the files as one stream")
	pad := flag.Duration("pad", 1500*time.Millisecond, "silence fed before and after each file when -fresh")
	channel := flag.Int("channel", 0, "channel to score in a multi-channel WAV; -1 averages them")
	agree := flag.Duration("agree", 1500*time.Millisecond, "two models agree when they fire within this of each other")
	trace := flag.String("trace", "", "write a CSV of time,score per file and model into this directory")
	verbose := flag.Bool("v", false, "print every detection and near miss with its time")
	front := flag.String("front", "approx", "microWakeWord feature front end: approx (zserge, the Dot's default) or exact (mwwfront, TECHO5_WAKE_FRONTEND=exact)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: wakescore -models DIR [flags] FILE|DIR...")
		flag.PrintDefaults()
	}
	flag.Parse()

	switch *front {
	case "approx":
	case "exact":
		exactFrontEnd = true
	default:
		fail(fmt.Errorf("-front is approx or exact, not %q", *front))
	}
	if *modelDir == "" || flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	models, err := pick(*modelDir, *wakeIDs)
	if err != nil {
		fail(err)
	}
	cutoffs, err := thresholds(models, *threshold, *overrides)
	if err != nil {
		fail(err)
	}
	files, err := collect(flag.Args())
	if err != nil {
		fail(err)
	}
	if *trace != "" {
		if err := os.MkdirAll(*trace, 0o755); err != nil {
			fail(err)
		}
	}

	fmt.Printf("%d models, %d files, threshold %.2f\n", len(models), len(files), *threshold)
	for _, m := range models {
		fmt.Printf("  %-24s %-14s %q  cutoff %.2f\n", m.ID, m.Kind, m.Phrase, cutoffs[m.ID])
	}
	fmt.Println()

	var (
		s       *session
		results []*fileResult
		audio   time.Duration
		began   = time.Now()
	)
	for i, path := range files {
		pcm, err := readWAV(path, *channel)
		if err != nil {
			fail(fmt.Errorf("%s: %w", path, err))
		}
		audio += time.Duration(len(pcm)) * time.Second / rate

		if s == nil || *fresh {
			s, err = newSession(models, cutoffs)
			if err != nil {
				fail(err)
			}
		}
		if *fresh {
			s.feed(make([]int16, int(pad.Seconds()*rate)))
		}
		r := s.begin(i, path, time.Duration(len(pcm))*time.Second/rate)
		s.feed(pcm)
		if *fresh {
			s.feed(make([]int16, int(pad.Seconds()*rate)))
			s.finish()
		}
		results = append(results, r)

		if *trace != "" {
			if err := s.writeTrace(*trace, r); err != nil {
				fail(err)
			}
		}
		s.clearTrace()
	}
	if s != nil && !*fresh {
		s.finish()
	}
	took := time.Since(began)

	report(models, results, *verbose)
	summary(models, results, audio, *agree)
	fmt.Printf("\n%s of audio scored in %s (%.0fx real time)\n",
		audio.Round(time.Second), took.Round(time.Millisecond), audio.Seconds()/took.Seconds())
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "wakescore:", err)
	os.Exit(1)
}

// pick reads the model directory the way the Dot does and keeps the ids asked for.
func pick(dir, ids string) ([]wake.Model, error) {
	installed, err := wake.Installed(dir)
	if err != nil {
		return nil, err
	}
	if len(installed) == 0 {
		return nil, fmt.Errorf("no .tflite models in %s", dir)
	}
	if ids == "" {
		return installed, nil
	}
	var out []wake.Model
	for _, id := range strings.Split(ids, ",") {
		id = strings.TrimSpace(id)
		m, ok := wake.Find(installed, id)
		if !ok {
			return nil, fmt.Errorf("no model %q in %s", id, dir)
		}
		out = append(out, m)
	}
	return out, nil
}

func thresholds(models []wake.Model, base float64, overrides string) (map[string]float64, error) {
	out := map[string]float64{}
	for _, m := range models {
		out[m.ID] = base
	}
	if overrides == "" {
		return out, nil
	}
	for _, kv := range strings.Split(overrides, ",") {
		id, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok {
			return nil, fmt.Errorf("-thresholds: %q is not id=value", kv)
		}
		if _, known := out[id]; !known {
			return nil, fmt.Errorf("-thresholds: %q is not a loaded model", id)
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("-thresholds: %q: %w", kv, err)
		}
		out[id] = f
	}
	return out, nil
}

// collect lists the WAV files named, walking directories, in a stable order.
func collect(args []string) ([]string, error) {
	var out []string
	for _, a := range args {
		info, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			out = append(out, a)
			continue
		}
		err = filepath.WalkDir(a, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".wav") {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil, errors.New("no .wav files found")
	}
	return out, nil
}

// engine is one of the two backends, as internal/feature/detect wraps them: every frame is fed, and a
// backend that scores less often than once per frame says so.
type engine interface {
	feed(frame []int16) (map[string]float64, bool)
}

type microEngine struct {
	dets   map[string]*microwakeword.Detector
	fronts map[string]*exactFront // -front exact: each model's mwwfront, as detect's microFront
	scores map[string]float64
}

// exactFront is mwwfront and the audio it has not read yet, for -front exact: the daemon's
// TECHO5_WAKE_FRONTEND=exact path (detect.microBackend with exact set), repeated here for the same
// reason as the judge's timings.
type exactFront struct {
	fe  *mwwfront.Frontend
	buf []int16
}

// exactFrontEnd is -front exact. False is zserge's audiofrontend inside the detector, what the Dot
// runs by default.
var exactFrontEnd bool

func newMicro(models []wake.Model) (e *microEngine, err error) {
	e = &microEngine{dets: map[string]*microwakeword.Detector{}, fronts: map[string]*exactFront{}, scores: map[string]float64{}}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("damaged microWakeWord model: %v", r)
		}
	}()
	for _, m := range models {
		cfg := m.Config
		cfg.ProbabilityCutoff = neverFires
		det, err := microwakeword.NewDetector(cfg)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.ID, err)
		}
		e.dets[m.ID] = det
		if exactFrontEnd {
			e.fronts[m.ID] = &exactFront{fe: mwwfront.New(mwwfront.StepMs(cfg.FeaturesStepMs))}
		}
	}
	return e, nil
}

func (e *microEngine) feed(frame []int16) (map[string]float64, bool) {
	for id, det := range e.dets {
		if f := e.fronts[id]; f != nil {
			f.buf = append(f.buf, frame...)
			win, step := f.fe.WindowSamples(), f.fe.StepSamples()
			for len(f.buf) >= win {
				feat := make([]int8, mwwfront.Channels)
				mwwfront.Quantize(f.fe.ProcessFrame(f.buf[:win]), feat)
				det.ProcessFeatures([][]int8{feat})
				f.buf = f.buf[step:]
			}
		} else {
			det.ProcessAudio(frame)
		}
		e.scores[id] = det.SlidingAverage()
	}
	return e.scores, true
}

type owwEngine struct {
	front  *oww.Engine
	scores map[string]float64
}

func newOWW(models []wake.Model) (*owwEngine, error) {
	front, err := oww.New()
	if err != nil {
		return nil, err
	}
	e := &owwEngine{front: front, scores: map[string]float64{}}
	for _, m := range models {
		raw, err := os.ReadFile(m.Path)
		if err != nil {
			return nil, err
		}
		if _, err := front.Load(m.ID, raw); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func (e *owwEngine) feed(frame []int16) (map[string]float64, bool) {
	scores, err := e.front.Process(frame)
	if err != nil {
		fail(err)
	}
	if len(scores) == 0 {
		return nil, false
	}
	for id, v := range scores {
		e.scores[id] = float64(v)
	}
	return e.scores, true
}

// judge is one slot's reading of its scores, as detect.Engine.judge does it, on a clock of frames.
type judge struct {
	id     string
	cutoff float64

	quiet    time.Duration
	peak     float64
	peakAt   time.Duration
	holding  bool
	holdEnds time.Duration

	file *fileResult
	best float64
}

type hit struct {
	at             time.Duration
	crossing, peak float64
}

type miss struct {
	at   time.Duration
	peak float64
}

// perModel is what one model did in one file.
type perModel struct {
	best   float64
	hits   []hit
	misses []miss
}

type fileResult struct {
	index   int
	path    string
	length  time.Duration
	start   time.Duration
	byModel map[string]*perModel
}

func (j *judge) score(s float64, now time.Duration) {
	pm := j.file.byModel[j.id]
	if s > pm.best {
		pm.best = s
	}

	if now < j.quiet {
		return
	}
	if j.holding {
		if s > j.peak {
			j.peak = s
			// The hit belongs to the file it fired in; a hold that runs past a file boundary in
			// stream mode has nothing to update here.
			if n := len(pm.hits); n > 0 {
				pm.hits[n-1].peak = s
			}
		}
		if now < j.holdEnds {
			return
		}
		j.holding, j.peak = false, 0
		j.quiet = now + refractory
		return
	}
	if s < j.cutoff {
		if s > j.peak {
			j.peak, j.peakAt = s, now
		}
		if j.peak >= nearMiss && now-j.peakAt > nearMissSettle {
			pm.misses = append(pm.misses, miss{at: j.peakAt - j.file.start, peak: j.peak})
			j.peak = 0
		}
		return
	}

	// The detection fires at the crossing; the hold only finds the peak for the record.
	j.holding, j.holdEnds = true, now+hold
	j.peak = s
	pm.hits = append(pm.hits, hit{at: now - j.file.start, crossing: s, peak: s})
}

// finish reports a near miss still settling when the audio ran out.
func (j *judge) finish() {
	if j.file == nil || j.holding {
		return
	}
	if j.peak >= nearMiss {
		pm := j.file.byModel[j.id]
		pm.misses = append(pm.misses, miss{at: j.peakAt - j.file.start, peak: j.peak})
	}
	j.peak = 0
}

// session is a set of engines and judges with a running clock: the whole of what the Dot runs
// between the microphones and the detection callback.
type session struct {
	engines map[wake.Kind]engine
	models  []wake.Model
	judges  map[string]*judge
	now     time.Duration
	cur     *fileResult

	// rest is audio short of a whole frame, kept for the next feed, as the mic would hand it on.
	rest []int16

	traces map[string][]float64
}

func newSession(models []wake.Model, cutoffs map[string]float64) (*session, error) {
	s := &session{
		engines: map[wake.Kind]engine{},
		models:  models,
		judges:  map[string]*judge{},
		traces:  map[string][]float64{},
	}
	for _, k := range []wake.Kind{wake.KindMicroWakeWord, wake.KindOpenWakeWord} {
		of := wake.OfKind(models, k)
		if len(of) == 0 {
			continue
		}
		var (
			e   engine
			err error
		)
		if k == wake.KindOpenWakeWord {
			e, err = newOWW(of)
		} else {
			e, err = newMicro(of)
		}
		if err != nil {
			return nil, err
		}
		s.engines[k] = e
	}
	for _, m := range models {
		s.judges[m.ID] = &judge{id: m.ID, cutoff: cutoffs[m.ID]}
	}
	return s, nil
}

// begin starts attributing what follows to a file.
func (s *session) begin(index int, path string, length time.Duration) *fileResult {
	r := &fileResult{index: index, path: path, length: length, start: s.now, byModel: map[string]*perModel{}}
	for _, m := range s.models {
		r.byModel[m.ID] = &perModel{}
	}
	// A near miss from the previous file that has not settled belongs to that file.
	for _, j := range s.judges {
		j.finish()
		j.file = r
	}
	s.cur = r
	return r
}

func (s *session) feed(pcm []int16) {
	s.rest = append(s.rest, pcm...)
	for len(s.rest) >= frameSamples {
		s.frame(s.rest[:frameSamples])
		s.rest = s.rest[frameSamples:]
	}
}

func (s *session) frame(frame []int16) {
	for _, m := range s.models {
		scores, fresh := s.engines[m.Kind].feed(frame)
		if !fresh {
			continue
		}
		score, ok := scores[m.ID]
		if !ok {
			continue
		}
		// The silence fed before a file only warms the engines; there is no file to judge it against.
		if s.cur == nil {
			continue
		}
		s.judges[m.ID].score(score, s.now)
		s.traces[m.ID] = append(s.traces[m.ID], score)
	}
	s.now += frameTime
}

func (s *session) finish() {
	for _, j := range s.judges {
		j.finish()
	}
}

func (s *session) clearTrace() {
	for id := range s.traces {
		s.traces[id] = s.traces[id][:0]
	}
}

// writeTrace saves every score a model produced while the file was being scored, at the time the
// Dot would have seen it. openWakeWord scores every 80 ms and microWakeWord every 20 ms, so the
// rows are timed by count against the file's own clock rather than by frame.
func (s *session) writeTrace(dir string, r *fileResult) error {
	base := strings.TrimSuffix(filepath.Base(r.path), filepath.Ext(r.path))
	for _, m := range s.models {
		scores := s.traces[m.ID]
		if len(scores) == 0 {
			continue
		}
		f, err := os.Create(filepath.Join(dir, base+"-"+m.ID+".csv"))
		if err != nil {
			return err
		}
		step := frameTime
		if m.Kind == wake.KindOpenWakeWord {
			step = 4 * frameTime
		}
		// The first score arrives after the engine's warm-up, so count back from the end.
		first := (s.now - r.start) - time.Duration(len(scores))*step
		for i, v := range scores {
			t := first + time.Duration(i)*step
			fmt.Fprintf(f, "%.3f,%.4f\n", t.Seconds(), v)
		}
		f.Close()
	}
	return nil
}

func report(models []wake.Model, results []*fileResult, verbose bool) {
	for _, r := range results {
		fmt.Printf("%-40s %6.1fs", short(r.path), r.length.Seconds())
		for _, m := range models {
			pm := r.byModel[m.ID]
			fmt.Printf("   %s: peak %.2f", m.ID, pm.best)
			if n := len(pm.hits); n > 0 {
				fmt.Printf(", %d HIT", n)
			}
			if n := len(pm.misses); n > 0 {
				fmt.Printf(", %d near", n)
			}
		}
		fmt.Println()
		if !verbose {
			continue
		}
		for _, m := range models {
			pm := r.byModel[m.ID]
			for _, h := range pm.hits {
				fmt.Printf("      %-24s HIT  at %6.2fs  crossed %.2f  peak %.2f\n", m.ID, h.at.Seconds(), h.crossing, h.peak)
			}
			for _, x := range pm.misses {
				fmt.Printf("      %-24s near at %6.2fs  peak %.2f\n", m.ID, x.at.Seconds(), x.peak)
			}
		}
	}
}

func summary(models []wake.Model, results []*fileResult, audio time.Duration, agree time.Duration) {
	hours := audio.Hours()
	fmt.Println()
	fmt.Printf("%-24s %6s %9s %6s %8s\n", "model", "hits", "per hour", "near", "files")
	for _, m := range models {
		var hits, near, files int
		for _, r := range results {
			pm := r.byModel[m.ID]
			hits += len(pm.hits)
			near += len(pm.misses)
			if len(pm.hits) > 0 {
				files++
			}
		}
		perHour := 0.0
		if hours > 0 {
			perHour = float64(hits) / hours
		}
		fmt.Printf("%-24s %6d %9.2f %6d %8d\n", m.ID, hits, perHour, near, files)
	}

	if len(models) < 2 {
		return
	}
	fmt.Println()
	for i, a := range models {
		for _, b := range models[i+1:] {
			ab, an := agreed(results, a.ID, b.ID, agree)
			ba, bn := agreed(results, b.ID, a.ID, agree)
			fmt.Printf("%s and %s agreed on %d of %s's %d hits and %d of %s's %d hits (within %s)\n",
				a.ID, b.ID, ab, a.ID, an, ba, b.ID, bn, agree)
		}
	}
}

// agreed counts a's hits that b also fired on, within window, in the same file.
func agreed(results []*fileResult, a, b string, window time.Duration) (both, total int) {
	for _, r := range results {
		for _, h := range r.byModel[a].hits {
			total++
			for _, o := range r.byModel[b].hits {
				d := h.at - o.at
				if d < 0 {
					d = -d
				}
				if d <= window {
					both++
					break
				}
			}
		}
	}
	return both, total
}

func short(path string) string {
	dir, base := filepath.Split(filepath.Clean(path))
	parent := filepath.Base(filepath.Clean(dir))
	if parent == "." || parent == string(filepath.Separator) {
		return base
	}
	return parent + "/" + base
}

// readWAV returns one channel of 16 kHz PCM from a WAV, or the average of all of them.
func readWAV(path string, channel int) ([]int16, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF WAVE file")
	}

	var (
		format, channels, bits int
		sampleRate             int
		data                   []byte
	)
	total := uint64(len(raw))
	for off := uint64(12); off+8 <= total; {
		id := string(raw[off : off+4])
		size := uint64(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
		off += 8
		end := min(off+size, total)
		switch id {
		case "fmt ":
			if end-off < 16 {
				return nil, errors.New("short fmt chunk")
			}
			format = int(binary.LittleEndian.Uint16(raw[off:]))
			channels = int(binary.LittleEndian.Uint16(raw[off+2:]))
			sampleRate = int(binary.LittleEndian.Uint32(raw[off+4:]))
			bits = int(binary.LittleEndian.Uint16(raw[off+14:]))
			// WAVE_FORMAT_EXTENSIBLE carries the real format in its sub-format GUID's first two bytes.
			if format == 0xFFFE && end-off >= 26 {
				format = int(binary.LittleEndian.Uint16(raw[off+24:]))
			}
		case "data":
			data = raw[off:end]
		}
		off = end + size%2
	}
	if data == nil {
		return nil, errors.New("no data chunk")
	}
	if sampleRate != rate {
		return nil, fmt.Errorf("%d Hz, need %d (convert first: ffmpeg -i in.wav -ar 16000 -ac 1 out.wav)", sampleRate, rate)
	}
	if channels < 1 {
		return nil, errors.New("no channels")
	}
	if channel >= channels {
		return nil, fmt.Errorf("channel %d of %d", channel, channels)
	}

	bytesPer := bits / 8
	frames := len(data) / (bytesPer * channels)
	sample := func(i, c int) (float64, error) {
		p := data[(i*channels+c)*bytesPer:]
		switch {
		case format == 1 && bits == 16:
			return float64(int16(binary.LittleEndian.Uint16(p))), nil
		case format == 1 && bits == 24:
			v := int32(p[0]) | int32(p[1])<<8 | int32(p[2])<<16
			if v&0x800000 != 0 {
				v |= ^0xFFFFFF
			}
			return float64(v) / 256, nil
		case format == 1 && bits == 32:
			return float64(int32(binary.LittleEndian.Uint32(p))) / 65536, nil
		case format == 3 && bits == 32:
			f := float32FromBits(binary.LittleEndian.Uint32(p))
			return float64(f) * 32767, nil
		}
		return 0, fmt.Errorf("unsupported format %d at %d bits", format, bits)
	}

	out := make([]int16, frames)
	for i := range out {
		var v float64
		if channel < 0 {
			for c := 0; c < channels; c++ {
				x, err := sample(i, c)
				if err != nil {
					return nil, err
				}
				v += x
			}
			v /= float64(channels)
		} else {
			x, err := sample(i, channel)
			if err != nil {
				return nil, err
			}
			v = x
		}
		out[i] = int16(max(-32768, min(32767, v)))
	}
	return out, nil
}

func float32FromBits(b uint32) float32 {
	return math.Float32frombits(b)
}
