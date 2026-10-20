package main

import (
	"fmt"
	"io"
)

// noteExtras ends a plan that listed resources carrying things the stack does not declare (the "not in the stack" lines) with one sentence
// that says what that means: nothing is changed or removed because of it. Silent when there are none.
func noteExtras(out io.Writer, n int) {
	if n == 0 {
		return
	}
	who := "1 resource carries"
	if n != 1 {
		who = fmt.Sprintf("%d resources carry", n)
	}
	fmt.Fprintf(out, "note: %s devices, config keys or profiles that the stack does not declare (the \"not in the stack\" lines above).\n"+
		"      plan changes nothing because of it and apply never removes them; declare them to manage them, or ignore the line. See docs/plan-extras.md.\n", who)
}
