package tflite

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Tensor is a buffer plus its current shape. Data is NHWC row-major and flat; Shape describes
// how to read it. Integer tensors decode into I32 whatever their declared width, because in
// these models they only ever carry shapes, axes and permutations.
type Tensor struct {
	Name  string
	Type  TensorType
	Shape []int
	F32   []float32
	I32   []int32
	Const bool
}

func count(shape []int) int {
	n := 1
	for _, d := range shape {
		n *= d
	}
	return n
}

// Count is the number of elements the current shape describes.
func (t *Tensor) Count() int { return count(t.Shape) }

// Dim is the size of an axis, counting from the end for negative i.
func (t *Tensor) Dim(i int) int {
	if i < 0 {
		i += len(t.Shape)
	}
	if i < 0 || i >= len(t.Shape) {
		return 1
	}
	return t.Shape[i]
}

func (t *Tensor) resize(shape []int) {
	n := count(shape)
	t.Shape = append(t.Shape[:0], shape...)
	switch t.Type {
	case Float32:
		if cap(t.F32) < n {
			t.F32 = make([]float32, n)
		}
		t.F32 = t.F32[:n]
	default:
		if cap(t.I32) < n {
			t.I32 = make([]int32, n)
		}
		t.I32 = t.I32[:n]
	}
}

// Interpreter runs one subgraph. It is not safe for concurrent use: tensors are reused between
// invocations so that steady-state inference does not allocate.
type Interpreter struct {
	model   *Model
	graph   *Subgraph
	tensors []*Tensor

	// branches are the subgraphs each IF in this graph can run, prepared once so that taking a
	// branch allocates nothing. A graph with no IF has none.
	branches map[*OpDesc]*branches
}

// branches are one IF's two ways forward.
type branches struct {
	then, otherwise *Interpreter
}

// New prepares the model's first subgraph for execution, and through it every subgraph an IF in it
// can reach.
func New(m *Model) (*Interpreter, error) {
	return newGraph(m, 0, map[int]bool{})
}

// newGraph prepares one subgraph. building holds the subgraphs on the way to this one, so a graph
// that reaches itself through an IF is refused rather than descended into forever.
func newGraph(m *Model, idx int, building map[int]bool) (*Interpreter, error) {
	if idx < 0 || idx >= len(m.Subgraphs) {
		return nil, fmt.Errorf("tflite: no subgraph %d", idx)
	}
	if building[idx] {
		return nil, fmt.Errorf("tflite: subgraph %d reaches itself", idx)
	}
	building[idx] = true
	defer delete(building, idx)

	g := m.Subgraphs[idx]
	in := &Interpreter{model: m, graph: g, tensors: make([]*Tensor, len(g.Tensors))}

	for i, d := range g.Tensors {
		t := &Tensor{Name: d.Name, Type: d.Type, Shape: append([]int(nil), d.Shape...)}
		raw := m.buffer(d.Buffer)
		if raw != nil {
			if err := decode(t, raw); err != nil {
				return nil, fmt.Errorf("tensor %d (%s): %w", i, d.Name, err)
			}
			t.Const = true
		} else {
			switch d.Type {
			case Float32, Int32, Int64, Bool:
			default:
				return nil, fmt.Errorf("tflite: tensor %d (%s) has unsupported type %s", i, d.Name, d.Type)
			}
			t.resize(t.Shape)
		}
		in.tensors[i] = t
	}

	for i, o := range g.Ops {
		if !implemented(o.Op) {
			return nil, fmt.Errorf("tflite: operator %d is %s, which is not implemented", i, o.Op)
		}
		if o.Op != OpIf {
			continue
		}
		then, err := newGraph(m, o.thenGraph, building)
		if err != nil {
			return nil, fmt.Errorf("tflite: operator %d (IF), then branch: %w", i, err)
		}
		otherwise, err := newGraph(m, o.elseGraph, building)
		if err != nil {
			return nil, fmt.Errorf("tflite: operator %d (IF), else branch: %w", i, err)
		}
		if in.branches == nil {
			in.branches = map[*OpDesc]*branches{}
		}
		in.branches[o] = &branches{then: then, otherwise: otherwise}
	}
	return in, nil
}

// implemented reports whether an operator can run: it has a kernel, or it is the control operator
// the interpreter handles itself.
func implemented(op Op) bool {
	if op == OpIf {
		return true
	}
	_, ok := kernels[op]
	return ok
}

func decode(t *Tensor, raw []byte) error {
	switch t.Type {
	case Float32:
		n := len(raw) / 4
		t.F32 = make([]float32, n)
		for i := range t.F32 {
			t.F32[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
		}
	case Int32:
		n := len(raw) / 4
		t.I32 = make([]int32, n)
		for i := range t.I32 {
			t.I32[i] = int32(binary.LittleEndian.Uint32(raw[4*i:]))
		}
	case Int64:
		n := len(raw) / 8
		t.I32 = make([]int32, n)
		for i := range t.I32 {
			t.I32[i] = int32(int64(binary.LittleEndian.Uint64(raw[8*i:])))
		}
	case Bool:
		t.I32 = make([]int32, len(raw))
		for i, b := range raw {
			if b != 0 {
				t.I32[i] = 1
			}
		}
	default:
		return fmt.Errorf("unsupported constant type %s", t.Type)
	}
	return nil
}

// Tensor returns a tensor by graph index.
func (in *Interpreter) Tensor(i int) *Tensor { return in.tensors[i] }

// Input returns the i'th subgraph input.
func (in *Interpreter) Input(i int) *Tensor { return in.tensors[in.graph.Inputs[i]] }

// Output returns the i'th subgraph output.
func (in *Interpreter) Output(i int) *Tensor { return in.tensors[in.graph.Outputs[i]] }

// ResizeInput sets an input's shape. Every other shape follows from it during Invoke, so this is
// the only place a caller has to think about dynamic dimensions.
func (in *Interpreter) ResizeInput(i int, shape []int) {
	in.Input(i).resize(shape)
}

// Invoke runs every operator in order, recomputing shapes as it goes.
func (in *Interpreter) Invoke() error {
	for i, o := range in.graph.Ops {
		ins := make([]*Tensor, len(o.Inputs))
		for j, idx := range o.Inputs {
			// An optional input, such as a missing bias, is encoded as index -1.
			if idx >= 0 {
				ins[j] = in.tensors[idx]
			}
		}
		outs := make([]*Tensor, len(o.Outputs))
		for j, idx := range o.Outputs {
			outs[j] = in.tensors[idx]
		}
		var err error
		if o.Op == OpIf {
			err = in.runIf(o, ins, outs)
		} else {
			err = kernels[o.Op](o, ins, outs)
		}
		if err != nil {
			return fmt.Errorf("tflite: op %d (%s): %w", i, o.Op, err)
		}
	}
	return nil
}

// runIf takes one of an IF's two branches. The condition is the first input; the rest become the
// chosen subgraph's inputs, and its outputs come back as the IF's own.
//
// The inputs are handed over by reference rather than copied. A subgraph only reads its inputs,
// and a two-stage wake word model passes its verifier's whole weight set through here on every
// step, which would be 400 KB of copying for the branch that then does nothing with it.
func (in *Interpreter) runIf(o *OpDesc, ins, outs []*Tensor) error {
	br := in.branches[o]
	if br == nil {
		return errors.New("no branches were prepared")
	}
	cond := ins[0]
	if cond == nil || len(cond.I32) == 0 {
		return errors.New("the condition is empty")
	}
	branch := br.otherwise
	if cond.I32[0] != 0 {
		branch = br.then
	}

	args := ins[1:]
	if len(args) != len(branch.graph.Inputs) {
		return fmt.Errorf("%d inputs handed to a subgraph that takes %d", len(args), len(branch.graph.Inputs))
	}
	for i, a := range args {
		if a == nil {
			return fmt.Errorf("input %d is missing", i+1)
		}
		idx := branch.graph.Inputs[i]
		if want := branch.graph.Tensors[idx].Type; storage(a.Type) != storage(want) {
			return fmt.Errorf("input %d is %s, the subgraph wants %s", i+1, a.Type, want)
		}
		branch.tensors[idx] = a
	}

	if err := branch.Invoke(); err != nil {
		return err
	}

	if len(outs) != len(branch.graph.Outputs) {
		return fmt.Errorf("%d outputs expected, the subgraph produces %d", len(outs), len(branch.graph.Outputs))
	}
	for i, y := range outs {
		src := branch.Output(i)
		if storage(src.Type) != storage(y.Type) {
			return fmt.Errorf("output %d is %s, the subgraph produced %s", i, y.Type, src.Type)
		}
		y.resize(src.Shape)
		copyData(y, src)
	}
	return nil
}

// storage is which of a tensor's two buffers its type is kept in.
func storage(t TensorType) TensorType {
	if t == Float32 {
		return Float32
	}
	return Int32
}

type kernel func(o *OpDesc, in, out []*Tensor) error

var kernels map[Op]kernel

func activate(v float32, a Activation) float32 {
	switch a {
	case ActRelu:
		if v < 0 {
			return 0
		}
	case ActRelu1:
		if v < -1 {
			return -1
		}
		if v > 1 {
			return 1
		}
	case ActRelu6:
		if v < 0 {
			return 0
		}
		if v > 6 {
			return 6
		}
	case ActTanh:
		return float32(math.Tanh(float64(v)))
	}
	return v
}

// padFor is TFLite's padding split for SAME: the total padding is distributed with the smaller
// half before the data.
func padFor(stride, dilation, in, filter, out int) int {
	effective := (filter-1)*dilation + 1
	p := ((out-1)*stride + effective - in) / 2
	if p < 0 {
		return 0
	}
	return p
}

func outputSize(in, filter, stride, dilation int, samePad bool) int {
	if samePad {
		return (in + stride - 1) / stride
	}
	effective := (filter-1)*dilation + 1
	if in < effective {
		return 0
	}
	return (in-effective)/stride + 1
}
