package utils

import (
	"sync"
	"time"
)

type Deduper struct {
	mu   sync.Mutex
	data map[string]time.Time
	ttl  time.Duration
}

func NewDeduper(ttl time.Duration) *Deduper {
	return &Deduper{
		data: make(map[string]time.Time),
		ttl:  ttl,
	}
}

func (d *Deduper) Hit(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	if exp, ok := d.data[key]; ok && exp.After(now) {
		// เคยมีอยู่และยังไม่หมดอายุ → ถือว่าซ้ำ
		return true
	}
	// set ใหม่
	d.data[key] = now.Add(d.ttl)
	return false
}
