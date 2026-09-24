package tflite

import "fmt"

// compare builds a kernel that tests two tensors element by element and writes 1 or 0 into a
// boolean output, broadcasting the way the arithmetic kernels do. Either side may be float or
// integer: each value is read as a float64, which holds any int32 exactly, so an integer against a
// float compares as TensorFlow Lite's own runtime compares them after its cast.
//
// A wake word model uses a comparison to choose between subgraphs: openWakeWord's two-stage
// classifiers test the first network's score against a constant and hand the answer to IF, which
// runs the verifier network only when the first one thought it heard something.
func compare(f func(a, b float64) bool) kernel {
	return func(o *OpDesc, in, out []*Tensor) error {
		a, b, y := in[0], in[1], out[0]
		if y.Type == Float32 {
			return fmt.Errorf("comparison output %q is %s, not bool", y.Name, y.Type)
		}
		shape, err := broadcastShape(a.Shape, b.Shape)
		if err != nil {
			return err
		}
		y.resize(shape)

		as, bs := strides(a.Shape, shape), strides(b.Shape, shape)
		idx := make([]int, len(shape))
		ao, bo := 0, 0
		for i := range y.Count() {
			var r int32
			if f(a.scalar(ao), b.scalar(bo)) {
				r = 1
			}
			y.I32[i] = r
			ao, bo = step(idx, shape, as, bs, ao, bo)
		}
		return nil
	}
}

// scalar reads one element whichever buffer the tensor's type keeps it in.
func (t *Tensor) scalar(i int) float64 {
	if t.Type == Float32 {
		return float64(t.F32[i])
	}
	return float64(t.I32[i])
}
