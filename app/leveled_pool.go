package main

import "sync"

type LeveledPool struct {
	// Tiers: 4KB, 64KB, 512KB, 2MB
	pools [4]sync.Pool
	sizes [4]int
}

func NewLeveledPool() *LeveledPool {
	lp := &LeveledPool{
		sizes: [4]int{4096, 65536, 524288, 2097152},
	}
	for i := range lp.pools {
		size := lp.sizes[i]
		lp.pools[i].New = func() any {
			b := make([]byte, size)
			return &b // Pool pointers to avoid interface allocation
		}
	}
	return lp
}

func (lp *LeveledPool) Get(size int) *[]byte {
	for i, bucketSize := range lp.sizes {
		if size <= bucketSize {
			ptr := lp.pools[i].Get().(*[]byte)
			*ptr = (*ptr)[:size]
			return ptr
		}
	}
	// Fallback for massive objects (larger than 2MB)
	b := make([]byte, size)
	return &b
}

func (lp *LeveledPool) Put(b *[]byte) {
	size := cap(*b)
	for i, bucketSize := range lp.sizes {
		if size == bucketSize {
			lp.pools[i].Put(b)
			return
		}
	}
}
