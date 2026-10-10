package system

import (
	"crypto/rand"
	"io"
	"sync"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
)

// crockford 是 Crockford base32 字母表（无 I L O U），按字符序也是数值序。
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const (
	randBytes       = 10 // 80 位随机数
	idChars         = 26 // 128 位编码为 26 个字符（最高字符只用 3 位）
	maxMilliseconds = 1<<48 - 1
)

// IDGen 生成 "<kind>_<26 位 ULID 风格编码>"：前 48 位是毫秒时间，后 80 位来自 crypto/rand。
// 同一毫秒内把随机部分加一，所以单调递增；时钟回拨时沿用上一次的毫秒。并发安全。
type IDGen struct {
	mu          sync.Mutex
	now         func() time.Time
	rnd         io.Reader
	initialized bool
	lastMs      uint64
	last        [randBytes]byte
}

var _ ports.IDGen = (*IDGen)(nil)

// NewIDGen 返回使用系统时间和 crypto/rand 的 ID 生成器。
func NewIDGen() *IDGen { return NewIDGenWith(time.Now, rand.Reader) }

// NewIDGenWith 注入时间源和随机源，供测试使用。
func NewIDGenWith(now func() time.Time, rnd io.Reader) *IDGen {
	if now == nil {
		panic("system: nil time source")
	}
	if rnd == nil {
		panic("system: nil random source")
	}
	return &IDGen{now: now, rnd: rnd}
}

// NewID 返回新 ID。随机源读取失败、时间不可表示或时间戳耗尽时以固定文本 panic。
// 端口没有 error 返回值；不能用不完整的随机数降级，也不把底层错误写进 panic。
func (g *IDGen) NewID(kind string) string {
	if kind == "" {
		kind = "id"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	wall := g.now()
	// 先检查 time.Time 的范围，避免极远日期的 UnixMilli 算术溢出后被当作合法时间。
	if wall.Before(time.UnixMilli(0)) || !wall.Before(time.UnixMilli(maxMilliseconds+1)) {
		panic("system: ID time outside 48-bit range")
	}
	ms, r := uint64(wall.UnixMilli()), g.last
	switch {
	case !g.initialized || ms > g.lastMs:
		if _, err := io.ReadFull(g.rnd, r[:]); err != nil {
			panic("system: random source failed")
		}
	default:
		// 同一毫秒（或时钟回拨）：随机部分加一；溢出时进到下一毫秒。
		ms = g.lastMs
		if !increment(&r) {
			if ms == maxMilliseconds {
				panic("system: ID timestamp exhausted")
			}
			ms++
			if _, err := io.ReadFull(g.rnd, r[:]); err != nil {
				panic("system: random source failed")
			}
		}
	}
	// 随机读取成功后才提交状态；失败后即使上层 recover，也不会复用半成品。
	g.lastMs, g.last, g.initialized = ms, r, true
	return kind + "_" + encode(ms, r)
}

// increment 把 80 位数加一，溢出返回 false。
func increment(b *[randBytes]byte) bool {
	for i := randBytes - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return true
		}
	}
	return false
}

// encode 把 48 位时间和 80 位随机数编码为 26 个字符（共 128 位，高位补零）。
func encode(ms uint64, r [randBytes]byte) string {
	var raw [16]byte
	raw[0], raw[1], raw[2] = byte(ms>>40), byte(ms>>32), byte(ms>>24)
	raw[3], raw[4], raw[5] = byte(ms>>16), byte(ms>>8), byte(ms)
	copy(raw[6:], r[:])

	var out [idChars]byte
	// 从低位起每 5 位一个字符；128 位 = 25*5 + 3。
	var acc uint
	var bits uint
	pos := idChars - 1
	for i := len(raw) - 1; i >= 0; i-- {
		acc |= uint(raw[i]) << bits
		bits += 8
		for bits >= 5 {
			out[pos] = crockford[acc&31]
			pos--
			acc >>= 5
			bits -= 5
		}
	}
	if bits > 0 {
		out[pos] = crockford[acc&31]
	}
	return string(out[:])
}
