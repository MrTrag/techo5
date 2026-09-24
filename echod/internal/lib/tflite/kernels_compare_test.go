package tflite

import "testing"

func tb() *Tensor { return &Tensor{Type: Bool} }

func TestGreaterEqualAgainstAScalar(t *testing.T) {
	y := tb()
	err := kernels[OpGreaterEqual](&OpDesc{}, []*Tensor{tf([]int{1, 3}, 0.2, 0.5, 0.9), tf([]int{}, 0.5)}, []*Tensor{y})
	if err != nil {
		t.Fatal(err)
	}
	shape(t, "shape", y.Shape, []int{1, 3})
	i32(t, "values", y.I32, []int32{0, 1, 1})
}

func TestComparisonsBroadcastAcrossRows(t *testing.T) {
	a := tf([]int{2, 2}, 1, 2, 3, 4)
	b := tf([]int{2}, 2, 2)
	for _, c := range []struct {
		op   Op
		want []int32
	}{
		{OpLess, []int32{1, 0, 0, 0}},
		{OpLessEqual, []int32{1, 1, 0, 0}},
		{OpGreater, []int32{0, 0, 1, 1}},
		{OpGreaterEqual, []int32{0, 1, 1, 1}},
		{OpEqual, []int32{0, 1, 0, 0}},
		{OpNotEqual, []int32{1, 0, 1, 1}},
	} {
		y := tb()
		if err := kernels[c.op](&OpDesc{}, []*Tensor{a, b}, []*Tensor{y}); err != nil {
			t.Fatal(c.op, err)
		}
		shape(t, c.op.String(), y.Shape, []int{2, 2})
		i32(t, c.op.String(), y.I32, c.want)
	}
}

func TestComparisonReadsIntegersToo(t *testing.T) {
	y := tb()
	if err := kernels[OpEqual](&OpDesc{}, []*Tensor{ti([]int{2}, 3, 4), tf([]int{2}, 3, 5)}, []*Tensor{y}); err != nil {
		t.Fatal(err)
	}
	i32(t, "values", y.I32, []int32{1, 0})
}

func TestComparisonRefusesAFloatOutput(t *testing.T) {
	if err := kernels[OpLess](&OpDesc{}, []*Tensor{tf([]int{1}, 1), tf([]int{1}, 2)}, []*Tensor{out()}); err == nil {
		t.Fatal("a comparison wrote into a float tensor")
	}
}
