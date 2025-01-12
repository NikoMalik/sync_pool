// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync

import (
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/NikoMalik/sync_pool/internal/race"
	"github.com/NikoMalik/sync_pool/internal/rtype"
)

func zeroValue[T any]() T {
	var zero T
	return zero
}

type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

type EmptyInterface struct {
	typ *rtype.Type
	ptr unsafe.Pointer
}

func isNil[T any](t T) bool {
	v := reflect.ValueOf(t)

	return v.IsZero()
}

type Pool[T any] struct {
	noCopy noCopy
	// New optionally specifies a function to generate
	// a value when Get would otherwise return nil.
	// It may not be changed concurrently with calls to Get.
	New func() T
	_   [64 - unsafe.Sizeof(func() T { var z T; return z })]byte

	size uintptr
	_    [64 - unsafe.Sizeof(uintptr(0))]byte

	local     unsafe.Pointer // local fixed-size per-P pool, actual type is [P]poolLocal
	_         [64 - unsafe.Sizeof(unsafe.Pointer(nil))]byte
	localSize uintptr // size of the local array
	_         [64 - unsafe.Sizeof(uintptr(0))]byte

	victim     unsafe.Pointer // local from previous cycle
	_          [64 - unsafe.Sizeof(unsafe.Pointer(nil))]byte
	victimSize uintptr // size of victims array
	_          [64 - unsafe.Sizeof(uintptr(0))]byte
}

// Local per-P Pool appendix.
type poolLocalInternal[T any] struct {
	private T            // Can be used only by the respective P.
	shared  poolChain[T] // Local P can pushHead/popHead; any P can popTail.
}

type poolLocal[T any] struct {
	poolLocalInternal[T]

	// Prevents false sharing on widespread platforms with
	// 128 mod (cache line size) = 0 .
	pad [128 - unsafe.Sizeof(unsafe.Sizeof(poolLocalInternal[T]{}))%128]byte
}

// // from runtime
// //
// //go:linkname runtime_randn runtime.randn
// func runtime_randn(n uint32) uint32
var poolRaceHash [128]uint64

// poolRaceAddr returns an address to use as the synchronization point
// for race detector logic. We don't use the actual pointer stored in x
// directly, for fear of conflicting with other synchronization on that address.
// Instead, we hash the pointer to get an index into poolRaceHash.
// See discussion on golang.org/cl/31589.
func poolRaceAddr(x any) unsafe.Pointer {
	ptr := uintptr((*[2]unsafe.Pointer)(unsafe.Pointer(&x))[1])
	h := uint32((uint64(uint32(ptr)) * 0x85ebca6b) >> 16)
	return unsafe.Pointer(&poolRaceHash[h%uint32(len(poolRaceHash))])
}

// Put adds x to the pool.
func (p *Pool[T]) Put(x T) {
	if isNil(x) {
		return
	}

	if race.Enabled {
		if fastrandn(4) == 0 {
			// Randomly drop x on the floor.
			return
		}
		race.ReleaseMerge(poolRaceAddr(x))
		race.Disable()
	}

	l, _ := p.pin()
	if isNil(x) {
		l.private = x
	} else {
		l.shared.pushHead(x)
	}
	runtime_procUnpin()

	if race.Enabled {
		race.Enable()
	}
}

// Get selects an arbitrary item from the [Pool], removes it from the
// Pool, and returns it to the caller.
// Get may choose to ignore the pool and treat it as empty.
// Callers should not assume any relation between values passed to [Pool.Put] and
// the values returned by Get.
//
// If Get would otherwise return nil and p.New is non-nil, Get returns
// the result of calling p.New.

func (p *Pool[T]) Get() T {
	if race.Enabled {
		race.Disable()
	}

	l, pid := p.pin()
	x := l.private
	var zero T
	l.private = zero
	if isNil(x) {
		x, _ = l.shared.popHead()
		if isNil(x) {
			x = p.getSlow(pid)
		}
	}
	runtime_procUnpin()

	if race.Enabled {
		race.Enable()
		if !isNil(x) {
			race.Acquire(poolRaceAddr(x))
		}
	}

	if isNil(x) && p.New != nil {
		return p.New()
	}
	return x
}

func (p *Pool[T]) getSlow(pid int) T {
	size := atomic.LoadUintptr(&p.localSize)
	locals := p.local

	for i := 0; i < int(size); i++ {
		l := indexLocal[T](locals, (pid+i+1)%int(size))
		if x, _ := l.shared.popTail(); !isNil(x) {
			return x
		}
	}

	size = atomic.LoadUintptr(&p.victimSize)
	if uintptr(pid) >= size {
		var zero T
		return zero
	}
	locals = p.victim
	l := indexLocal[T](locals, pid)
	if x := l.private; !isNil(x) {
		var zero T
		l.private = zero
		return x
	}

	for i := 0; i < int(size); i++ {
		l := indexLocal[T](locals, (pid+i)%int(size))
		if x, _ := l.shared.popTail(); &x != nil {
			return x
		}
	}

	atomic.StoreUintptr(&p.victimSize, 0)
	var zero T
	return zero
}

// pin pins the current goroutine to P, disables preemption and
// returns poolLocal pool for the P and the P's id.
// Caller must call runtime_procUnpin() when done with the pool.
func (p *Pool[T]) pin() (*poolLocal[T], int) {
	if p == nil {
		panic("pool is nil")

	}
	pid := runtime_procPin()
	s := atomic.LoadUintptr(&p.localSize)
	l := p.local
	if uintptr(pid) < s {
		return indexLocal[T](l, pid), pid
	}
	return p.pinSlow()
}

func (p *Pool[T]) pinSlow() (*poolLocal[T], int) {
	runtime_procUnpin()
	allPoolsMu.Lock()
	defer allPoolsMu.Unlock()

	pid := runtime_procPin()
	s := p.localSize
	l := p.local
	if uintptr(pid) < s {
		return indexLocal[T](l, pid), pid
	}
	if p.local == nil {
		allPools = append(allPools, (*Pool[any])(unsafe.Pointer(&p)))
	}
	size := runtime.GOMAXPROCS(0)
	local := make([]poolLocal[T], size)
	atomic.StorePointer(&p.local, unsafe.Pointer(&local[0]))
	atomic.StoreUintptr(&p.localSize, uintptr(size))

	return &local[pid], pid
}

// poolCleanup should be an internal detail,
// but widely used packages access it using linkname.
// Notable members of the hall of shame include:
//   - github.com/bytedance/gopkg
//   - github.com/songzhibin97/gkit
//
// Do not remove or change the type signature.
// See go.dev/issue/67401.
//
//go:linkname poolCleanup
func poolCleanup() {
	// This function is called with the world stopped, at the beginning of a garbage collection.
	// It must not allocate and probably should not call any runtime functions.

	// Because the world is stopped, no pool user can be in a
	// pinned section (in effect, this has all Ps pinned).

	// Drop victim caches from all pools.
	for _, p := range oldPools {
		p.victim = nil
		p.victimSize = 0
	}

	// Move primary cache to victim cache.
	for _, p := range allPools {
		p.victim = p.local
		p.victimSize = p.localSize
		p.local = nil
		p.localSize = 0
	}

	// The pools with non-empty primary caches now have non-empty
	// victim caches and no pools have primary caches.
	oldPools, allPools = allPools, nil
}

var (
	allPoolsMu sync.Mutex

	// allPools is the set of pools that have non-empty primary
	// caches. Protected by either 1) allPoolsMu and pinning or 2)
	// STW.
	allPools []*Pool[any]

	// oldPools is the set of pools that may have non-empty victim
	// caches. Protected by STW.
	oldPools []*Pool[any]
)

// func init() {
// 	runtime_registerPoolCleanup(poolCleanup)
// }

func indexLocal[T any](l unsafe.Pointer, i int) *poolLocal[T] {
	return (*poolLocal[T])(unsafe.Add(l, uintptr(i)*unsafe.Sizeof(poolLocal[T]{})))
}

func runtime_procPin() int {
	return ProcPin()
}

func runtime_procUnpin() {
	ProcUnpin()
}

// The below are implemented in internal/runtime/atomic and the
// compiler also knows to intrinsify the symbol we linkname into this
// package.

// //go:linkname runtime_LoadAcquintptr internal/runtime/atomic.LoadAcquintptr
// func LoadAcquintptr(ptr *uintptr) uintptr
//
// //go:linkname runtime_StoreReluintptr internal/runtime/atomic.StoreReluintptr
// func StoreReluintptr(ptr *uintptr, val uintptr) uintptr
