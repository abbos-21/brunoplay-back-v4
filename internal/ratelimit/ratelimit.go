// Package ratelimit is an in-process token-bucket limiter keyed by string (IP or player id).
// It sits behind an interface so a shared (Redis) limiter can replace it if one server is no longer enough.
package ratelimit

import (
	"hash/fnv"
	"sync"
	"time"
)

type Limiter interface {
	Allow(key string) bool
}

type bucket struct {
	tokens float64
	last   time.Time
}

type shard struct {
	mu sync.Mutex
	m  map[string]*bucket
}

// Local is a sharded token bucket: `rate` tokens per second, `burst` capacity.
type Local struct {
	rate   float64
	burst  float64
	shards [32]shard
	now    func() time.Time
}

func NewLocal(rate float64, burst int) *Local {
	l := &Local{rate: rate, burst: float64(burst), now: time.Now}
	for i := range l.shards {
		l.shards[i].m = map[string]*bucket{}
	}
	go l.janitor()
	return l
}

func (l *Local) Allow(key string) bool {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	sh := &l.shards[h.Sum32()%uint32(len(l.shards))]
	now := l.now()
	sh.mu.Lock()
	defer sh.mu.Unlock()
	b, ok := sh.m[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		sh.m[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// janitor drops idle buckets so the map cannot grow forever.
func (l *Local) janitor() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		cut := l.now().Add(-5 * time.Minute)
		for i := range l.shards {
			sh := &l.shards[i]
			sh.mu.Lock()
			for k, b := range sh.m {
				if b.last.Before(cut) {
					delete(sh.m, k)
				}
			}
			sh.mu.Unlock()
		}
	}
}
