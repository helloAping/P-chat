package agent

import "testing"

func TestExclusiveCallMaskKeepsParallelReads(t *testing.T) {
	// read, read, write, unknown
	canParallel := []bool{true, true, false, false}
	mask, anyExclusive := exclusiveCallMask(canParallel)
	if !anyExclusive {
		t.Fatal("expected an exclusive call")
	}
	if mask[0] || mask[1] || !mask[2] || !mask[3] {
		t.Fatalf("mask = %#v", mask)
	}
	_, any := exclusiveCallMask([]bool{true, true})
	if any {
		t.Fatal("all-parallel batch should not take a gate")
	}
}
