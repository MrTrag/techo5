package tflite

import (
	"bufio"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// twoWay is a model whose main graph is one IF: the then branch doubles its input, the else branch
// hands it back untouched, as the passthrough branch of a two-stage wake word model does.
func twoWay() *Model {
	f := func(name string, shape ...int) *TensorDesc {
		return &TensorDesc{Name: name, Type: Float32, Shape: shape}
	}
	return &Model{Subgraphs: []*Subgraph{
		{
			Name:    "main",
			Tensors: []*TensorDesc{{Name: "cond", Type: Bool}, f("x", 1, 3), f("y", 1, 3)},
			Inputs:  []int{0, 1},
			Outputs: []int{2},
			Ops:     []*OpDesc{{Op: OpIf, Inputs: []int{0, 1}, Outputs: []int{2}, thenGraph: 1, elseGraph: 2}},
		},
		{
			Name:    "double",
			Tensors: []*TensorDesc{f("a", 1, 3), f("b", 1, 3)},
			Inputs:  []int{0},
			Outputs: []int{1},
			Ops:     []*OpDesc{{Op: OpAdd, Inputs: []int{0, 0}, Outputs: []int{1}}},
		},
		{
			Name:    "same",
			Tensors: []*TensorDesc{f("a", 1, 3)},
			Inputs:  []int{0},
			Outputs: []int{0},
		},
	}}
}

func TestIfTakesTheBranchTheConditionSays(t *testing.T) {
	in, err := New(twoWay())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		cond int32
		want []float32
	}{
		{1, []float32{2, 4, 6}},
		{0, []float32{1, 2, 3}},
		{1, []float32{2, 4, 6}},
	} {
		in.Input(0).I32[0] = c.cond
		copy(in.Input(1).F32, []float32{1, 2, 3})
		if err := in.Invoke(); err != nil {
			t.Fatal(err)
		}
		shape(t, "output", in.Output(0).Shape, []int{1, 3})
		f32(t, "output", in.Output(0).F32, c.want)
	}
}

// A two-stage model squeezes its [1, 1] boolean to a scalar with a RESHAPE whose shape input is an
// empty vector, which means rank zero rather than no shape at all.
func TestReshapeToAScalarThroughAnEmptyShape(t *testing.T) {
	x := &Tensor{Type: Bool, Shape: []int{1, 1}, I32: []int32{1}}
	empty := &Tensor{Type: Int32, Shape: []int{0}}
	y := tb()
	if err := kernels[OpReshape](&OpDesc{}, []*Tensor{x, empty}, []*Tensor{y}); err != nil {
		t.Fatal(err)
	}
	shape(t, "scalar", y.Shape, []int{})
	i32(t, "value", y.I32, []int32{1})
}

func TestIfRefusesAGraphThatReachesItself(t *testing.T) {
	m := twoWay()
	m.Subgraphs[1].Ops = []*OpDesc{{Op: OpIf, Inputs: []int{0, 0}, Outputs: []int{1}, thenGraph: 0, elseGraph: 2}}
	if _, err := New(m); err == nil {
		t.Fatal("a subgraph that reaches itself was accepted")
	}
}

func TestIfRefusesAMissingBranch(t *testing.T) {
	m := twoWay()
	m.Subgraphs[0].Ops[0].thenGraph = 7
	if _, err := New(m); err == nil {
		t.Fatal("an IF pointing past the last subgraph was accepted")
	}
}

func TestIfChecksTheInputCount(t *testing.T) {
	m := twoWay()
	m.Subgraphs[0].Ops[0].Inputs = []int{0, 1, 1}
	in, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	in.Input(0).I32[0] = 1
	if err := in.Invoke(); err == nil {
		t.Fatal("two inputs were handed to a subgraph that takes one")
	}
}

// twoStageCase is one line of the fixture: what went in, what the first stage made of it, and what
// the model as a whole answered.
type twoStageCase struct {
	name   string
	input  []float32
	stage1 float32
	want   float32
}

// readTwoStageFixture reads testdata/generate_if_reference.py's output: a "case" line naming the
// case, how many input values follow (0 means referenceEmbeddings()), the first stage's score and
// the model's output, then the input values one per line.
func readTwoStageFixture(t *testing.T, path string) []twoStageCase {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()

	var cases []twoStageCase
	need := 0
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if fields := strings.Fields(line); fields[0] == "case" {
			if len(fields) != 5 {
				t.Fatalf("%s: bad case line %q", path, line)
			}
			n, err1 := strconv.Atoi(fields[2])
			s1, err2 := strconv.ParseFloat(fields[3], 32)
			w, err3 := strconv.ParseFloat(fields[4], 32)
			if err1 != nil || err2 != nil || err3 != nil {
				t.Fatalf("%s: bad case line %q", path, line)
			}
			c := twoStageCase{name: fields[1], stage1: float32(s1), want: float32(w)}
			if n == 0 {
				c.input = referenceEmbeddings()
			}
			cases = append(cases, c)
			need = n
			continue
		}
		if need == 0 {
			t.Fatalf("%s: unexpected line %q", path, line)
		}
		v, err := strconv.ParseFloat(line, 32)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		last := len(cases) - 1
		cases[last].input = append(cases[last].input, float32(v))
		need--
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	if need != 0 {
		t.Fatalf("%s: ended %d values short", path, need)
	}
	return cases
}

// TestTwoStageMatchesReferenceRuntime runs a two-stage openWakeWord classifier, the kind whose
// verifier sits behind an IF, against TensorFlow Lite's own runtime on two inputs: one the first
// stage rejects, so the IF hands its score straight through, and one that trips it, so the verifier
// runs and its score is the answer. TWO_STAGE names the model; testdata/generate_if_reference.py
// produced the fixture from it.
func TestTwoStageMatchesReferenceRuntime(t *testing.T) {
	model := os.Getenv("TWO_STAGE")
	if model == "" {
		t.Skip("set TWO_STAGE to a two-stage classifier testdata has a fixture for")
	}
	stem := strings.TrimSuffix(filepath.Base(model), ".tflite")
	cases := readTwoStageFixture(t, filepath.Join("testdata", "twostage_"+stem+"_reference.txt"))

	raw, err := os.ReadFile(model)
	if err != nil {
		t.Skip(err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	in, err := New(m)
	if err != nil {
		t.Fatal(err)
	}

	var tripped, passed int
	for _, c := range cases {
		if len(c.input) != in.Input(0).Count() {
			t.Fatalf("%s: %d input values for a model that takes %d", c.name, len(c.input), in.Input(0).Count())
		}
		copy(in.Input(0).F32, c.input)
		if err := in.Invoke(); err != nil {
			t.Fatal(err)
		}
		got := in.Output(0).F32[0]
		t.Logf("%-8s first stage %.6f  ours %.6f  reference %.6f", c.name, c.stage1, got, c.want)
		if math.Abs(float64(got-c.want)) > 1e-4 {
			t.Errorf("%s: our output differs from the reference by %.6g", c.name, got-c.want)
		}
		if c.stage1 >= 0.5 {
			tripped++
		} else {
			passed++
		}
	}
	if tripped == 0 || passed == 0 {
		t.Errorf("the fixture exercised only one branch (%d tripped, %d passed through)", tripped, passed)
	}
}
