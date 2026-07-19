package export

import "time"

// nowRFC3339UTC is a tiny indirection so the test
// suite can pin the export timestamp to a known value
// without monkey-patching time.Now. The test file
// overrides `nowFn` in the same package; production
// callers use the real time.Now.
var nowFn = func() time.Time { return time.Now().UTC() }

func nowRFC3339UTC() string {
	return nowFn().Format(time.RFC3339)
}
