package incusapi

import (
	"strings"
	"sync"
	"testing"
)

// Incus writes a command's stdout and stderr to the same buffer from two goroutines. The writes
// below, including empty ones (an idle stderr stream), must neither be lost nor race; run with -race.
func TestSyncBufferKeepsEverythingFromConcurrentWriters(t *testing.T) {
	for round := 0; round < 200; round++ {
		var b syncBuffer
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { // stdout: real data
			defer wg.Done()
			for i := 0; i < 50; i++ {
				b.Write([]byte("out\n"))
			}
		}()
		go func() { // stderr: mostly nothing, as for a command that succeeds
			defer wg.Done()
			for i := 0; i < 50; i++ {
				b.Write(nil)
				b.Write([]byte{})
			}
		}()
		wg.Wait()
		if got := strings.Count(b.String(), "out\n"); got != 50 {
			t.Fatalf("round %d: lost output: %d of 50 writes survived", round, got)
		}
	}
}
