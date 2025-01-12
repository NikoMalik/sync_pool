package sync

import (
	"runtime"
	"sync"
	"testing"
)

func TestPin(t *testing.T) {
	n := ProcPin()
	ProcUnpin()
	t.Log(n)
	var wg sync.WaitGroup
	for i := 0; i < runtime.GOMAXPROCS(0); i++ {
		wg.Add(1)
		go func() {
			n := ProcPin()
			ProcUnpin()
			t.Log(n)
			wg.Done()
		}()
	}
	wg.Wait()
}
