package coretest

import (
	"fmt"
	"sync"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
)

// FixedClock 是固定时间的时钟，只在 Advance 或 Set 时前进。
type FixedClock struct {
	mu sync.Mutex
	t  time.Time
}

var _ ports.Clock = (*FixedClock)(nil)

// NewFixedClock 从 start 开始；start 为零值时用 Epoch。
func NewFixedClock(start time.Time) *FixedClock {
	if start.IsZero() {
		start = Epoch
	}
	return &FixedClock{t: start}
}

func (c *FixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance 把时间往后拨 d。
func (c *FixedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// Set 把时间设为 t。
func (c *FixedClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// SeqIDGen 按 kind 分别顺序编号：call-1、call-2、event-1……
type SeqIDGen struct {
	mu   sync.Mutex
	next map[string]int
}

var _ ports.IDGen = (*SeqIDGen)(nil)

func NewSeqIDGen() *SeqIDGen { return &SeqIDGen{next: map[string]int{}} }

func (g *SeqIDGen) NewID(kind string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next[kind]++
	return fmt.Sprintf("%s-%d", kind, g.next[kind])
}
