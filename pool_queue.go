// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync

import (
	"sync/atomic"
	"unsafe"
)

// poolDequeue is a lock-free fixed-size single-producer,
// multi-consumer queue. The single producer can both push and pop
// from the head, and consumers can pop from the tail.
//
// It has the added feature that it nils out unused slots to avoid
// unnecessary retention of objects. This is important for sync.Pool,
// but not typically a property considered in the literature.
type poolDequeue[T any] struct {
	headTail atomic.Uint64
	vals     []atomic.Pointer[T]
}

type eface struct {
	typ, val unsafe.Pointer
}

const dequeueBits = 32

// dequeueLimit is the maximum size of a poolDequeue.
//
// This must be at most (1<<dequeueBits)/2 because detecting fullness
// depends on wrapping around the ring buffer without wrapping around
// the index. We divide by 4 so this fits in an int on 32-bit.
const dequeueLimit = (1 << dequeueBits) / 4

// dequeueNil is used in poolDequeue to represent interface{}(nil).
// Since we use nil to represent empty slots, we need a sentinel value
// to represent nil.
type dequeueNil *struct{}

func (d *poolDequeue[T]) unpack(ptrs uint64) (head, tail uint32) {
	const mask = 1<<dequeueBits - 1
	head = uint32((ptrs >> dequeueBits) & mask)
	tail = uint32(ptrs & mask)
	return
}

func (d *poolDequeue[T]) pack(head, tail uint32) uint64 {
	const mask = 1<<dequeueBits - 1
	return (uint64(head) << dequeueBits) | uint64(tail&mask)
}

// pushHead adds val at the head of the queue. It returns false if the
// queue is full. It must only be called by a single producer.
func (d *poolDequeue[T]) pushHead(val T) bool {
	ptrs := d.headTail.Load()
	head, tail := d.unpack(ptrs)

	if (tail+uint32(len(d.vals)))&(1<<dequeueBits-1) == head {
		return false
	}

	slot := &d.vals[head&uint32(len(d.vals)-1)]
	if slot.Load() != nil { // Проверяем, что слот пуст
		return false
	}
	if isNil(val) {
		val = *(*T)(unsafe.Pointer(dequeueNil(nil)))
	}
	*(*T)(unsafe.Pointer(slot)) = val

	// Устанавливаем значение потокобезопасно
	// slot.Store(&val)
	d.headTail.Add(1 << dequeueBits)
	return true
}

// popHead removes and returns the element at the head of the queue.
// It returns false if the queue is empty. It must only be called by a
// single producer.
func (d *poolDequeue[T]) popHead() (T, bool) {
	var zero *T
	var slot *atomic.Pointer[T]
	for {
		ptrs := d.headTail.Load()
		head, tail := d.unpack(ptrs)

		if tail == head {
			return *zero, false
		}

		head--
		ptrs2 := d.pack(head, tail)
		if d.headTail.CompareAndSwap(ptrs, ptrs2) {
			slot = &d.vals[head&uint32(len(d.vals)-1)]
			break
		}
	}

	val := *(*T)(unsafe.Pointer(slot))
	if isNil(val) {
		val = *(*T)(unsafe.Pointer(dequeueNil(nil)))
	}
	*slot = atomic.Pointer[T]{}
	return val, true
}

// popTail removes and returns the element at the tail of the queue.
// It returns false if the queue is empty. It may be called by any
// number of consumers.
func (d *poolDequeue[T]) popTail() (T, bool) {
	var zero T
	var slot *atomic.Pointer[T]
	for {
		ptrs := d.headTail.Load()
		head, tail := d.unpack(ptrs)

		if tail == head {
			return zero, false
		}

		ptrs2 := d.pack(head, tail+1)
		if d.headTail.CompareAndSwap(ptrs, ptrs2) {
			slot = &d.vals[tail&uint32(len(d.vals)-1)]
			break

		}
	}

	val := *(*T)(unsafe.Pointer(slot))
	if isNil(val) {
		val = *(*T)(unsafe.Pointer(dequeueNil(nil)))
	}
	slot = nil
	slot.Store(nil)
	return val, true
}

// poolChain is a dynamically-sized version of poolDequeue.
//
// This is implemented as a doubly-linked list queue of poolDequeues
// where each dequeue is double the size of the previous one. Once a
// dequeue fills up, this allocates a new one and only ever pushes to
// the latest dequeue. Pops happen from the other end of the list and
// once a dequeue is exhausted, it gets removed from the list.

type poolChainElt[T any] struct {
	poolDequeue[T]
	next, prev atomic.Pointer[poolChainElt[T]]
}

type poolChain[T any] struct {
	head *poolChainElt[T]
	tail atomic.Pointer[poolChainElt[T]]
}

func (c *poolChain[T]) pushHead(val T) {
	d := c.head
	if d == nil {
		const initSize = 8
		d = new(poolChainElt[T])
		d.vals = make([]atomic.Pointer[T], initSize)
		c.head = d
		c.tail.Store(d)
	}
	if d.pushHead(val) {
		return
	}

	newSize := len(d.vals) << 1
	if newSize >= dequeueLimit {
		newSize = dequeueLimit
	}
	d2 := &poolChainElt[T]{}
	d2.prev.Store(d)
	d2.vals = make([]atomic.Pointer[T], newSize)
	c.head = d2
	d.next.Store(d2)
	d2.pushHead(val)
}

func (c *poolChain[T]) popHead() (T, bool) {
	var zero T
	d := c.head
	for d != nil {
		if val, ok := d.popHead(); ok {
			return val, ok
		}
		d = d.prev.Load()
	}
	return zero, false
}

func (c *poolChain[T]) popTail() (T, bool) {
	var zero T
	d := c.tail.Load()
	if d == nil {
		return zero, false
	}
	for {
		d2 := d.next.Load()
		if val, ok := d.popTail(); ok {
			return val, ok
		}
		if d2 == nil {
			return zero, false
		}

		if c.tail.CompareAndSwap(d, d2) {
			d2.prev.Store(nil)
		}
		d = d2
	}
}
