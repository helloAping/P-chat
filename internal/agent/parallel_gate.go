package agent

// exclusiveCallMask marks tools that must take the serial gate.
// A true entry in canParallel stays false in the mask, so read-safe
// tools keep running together when the same batch also has a write.
// Unknown tools are passed as canParallel=false and stay exclusive.
func exclusiveCallMask(canParallel []bool) (mask []bool, anyExclusive bool) {
	mask = make([]bool, len(canParallel))
	for i, ok := range canParallel {
		if ok {
			continue
		}
		mask[i] = true
		anyExclusive = true
	}
	return mask, anyExclusive
}
