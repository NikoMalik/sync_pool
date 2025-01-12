package sync

import (
	s "sync"
	"testing"
)

type MyObject struct {
	ID int
}

func BenchmarkSyncPool(b *testing.B) {
	var pool = s.Pool{
		New: func() interface{} {
			return MyObject{}
		},
	}

	b.ResetTimer()

	b.Run("Get and Put", func(b *testing.B) {
		obj := pool.Get().(MyObject)
		for i := 0; i < b.N; i++ {
			obj.ID = i
			pool.Put(obj)
		}
	})

	b.Run("Get and Put async", func(b *testing.B) {
		obj := pool.Get().(MyObject)
		for i := 0; i < b.N; i++ {
			go func() {
				obj.ID = i
				pool.Put(obj)
			}()
		}
	})
}

func BenchmarkPoolMy(b *testing.B) {
	var pool = Pool[MyObject]{
		New: func() MyObject {
			return MyObject{}
		},
	}
	b.ResetTimer()

	b.Run("Get and Put", func(b *testing.B) {
		obj := pool.Get()
		for i := 0; i < b.N; i++ {
			obj.ID = i
			pool.Put(obj)
		}
	})

	b.Run("Get and Put async", func(b *testing.B) {
		obj := pool.Get()
		for i := 0; i < b.N; i++ {
			go func(i int) {
				obj.ID = i
				pool.Put(obj)
			}(i)
		}
	})
}
