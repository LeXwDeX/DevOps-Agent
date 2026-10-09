// Package system 提供 Clock 和 IDGen 的标准实现（62 第 4 节）。
package system

import (
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
)

// Clock 返回 UTC 时间，截断到微秒（和 PostgreSQL timestamptz 精度一致）。
// 截断同时去掉了单调时钟读数，所以结果可以直接用 == 或 DeepEqual 比较。
type Clock struct{}

var _ ports.Clock = Clock{}

// NewClock 返回系统时钟。
func NewClock() Clock { return Clock{} }

// Now 返回当前时间。
func (Clock) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
