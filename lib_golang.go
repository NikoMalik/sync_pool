package sync

import (
	"runtime"
	_ "unsafe"
)

//go:linkname runtime_Semacquire sync.runtime_Semacquire
func runtime_Semacquire(s *uint32)

//go:linkname runtime_SemacquireMutex sync.runtime_SemacquireMutex
func runtime_SemacquireMutex(s *uint32, lifo bool, skipframes int)

//go:linkname runtime_Semrelease sync.runtime_Semrelease
func runtime_Semrelease(s *uint32, handoff bool, skipframes int)

// //go:linkname sync_runtime_registerPoolCleanup runtime.sync_runtime_registerPoolCleanup
// func runtime_registerPoolCleanup(cleanup func())
//
//go:linkname fastrandn runtime.fastrandn
func fastrandn(n uint32) uint32

// Pin pins current p, return pid.
//
//go:linkname ProcPin runtime.procPin
func ProcPin() int

// Unpin unpins current p.
//
//go:linkname ProcUnpin runtime.procUnpin
func ProcUnpin()

// Pid returns the id of current p.
func Pid() (id int) {
	id = runtime_procPin()
	runtime_procUnpin()
	return
}

var multicore = runtime.NumCPU() > 1
