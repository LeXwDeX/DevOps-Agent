// Package system 提供 Clock 和 IDGen 的标准实现（62 第 4 节）。
package system

import (
	"sync"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
)

// Clock 返回 UTC 时间，截断到微秒（和 PostgreSQL timestamptz 精度一致）。
// 截断同时去掉了单调时钟读数，所以结果可以直接用 == 或 DeepEqual 比较。
// 系统时间回拨时返回上一次的时间，直到系统时间追上。零值可用，不应在首次使用后复制。
type Clock struct {
	mu   sync.Mutex
	now  func() time.Time
	last time.Time
}

var _ ports.Clock = (*Clock)(nil)

// NewClock 返回系统时钟。
func NewClock() *Clock { return &Clock{} }

// Now 返回非零时间；同一个实例上按调用顺序不倒退。可并发调用。
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	read := c.now
	if read == nil {
		read = time.Now
	}
	now := read().UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		panic("system: time source returned zero")
	}
	if !c.last.IsZero() && now.Before(c.last) {
		return c.last
	}
	c.last = now
	return now
}
