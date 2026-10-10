package modelgw

import (
	"bytes"
	"io"
	"strings"
)

// maskWriter 是流式 mask：把写入内容里每个 key 的精确值换成 mask 文本。
// 为了让跨块出现的 key 也被遮盖，它只留下"可能是 key 前缀"的尾部字节，
// 数量不超过 len(key)-1，Close 时输出剩余部分。
// 结果与 bytes.ReplaceAll(全文, key, mask) 一致。非并发安全。
type maskWriter struct {
	w    io.Writer
	key  []byte
	mask []byte
	held []byte // 上次留下的尾部
	work []byte // held + 本次输入
	out  []byte // 本次输出
}

func newMaskWriter(w io.Writer, key, mask []byte) *maskWriter {
	return &maskWriter{w: w, key: key, mask: mask}
}

// Write 遮盖后写出。每次调用最多对下游写一次。
func (m *maskWriter) Write(p []byte) (int, error) {
	if len(m.key) == 0 {
		return m.w.Write(p)
	}
	m.work = append(append(m.work[:0], m.held...), p...)
	buf := m.work
	out := m.out[:0]
	for {
		i := bytes.Index(buf, m.key)
		if i < 0 {
			break
		}
		out = append(out, buf[:i]...)
		out = append(out, m.mask...)
		buf = buf[i+len(m.key):]
	}
	keep := partialKeyLen(buf, m.key)
	out = append(out, buf[:len(buf)-keep]...)
	m.held = append(m.held[:0], buf[len(buf)-keep:]...)
	m.out = out
	if len(out) > 0 {
		n, err := m.w.Write(out)
		if err == nil && n != len(out) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Close 输出留下的尾部。它不是完整的 key，原样输出。
func (m *maskWriter) Close() error {
	if len(m.held) == 0 {
		return nil
	}
	b := m.held
	m.held = nil
	n, err := m.w.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}

// partialKeyLen 返回 buf 末尾最长的、是 key 真前缀的字节数。
// 上限为 len(key)-1。
func partialKeyLen(buf, key []byte) int {
	k := len(key) - 1
	if len(buf) < k {
		k = len(buf)
	}
	for ; k > 0; k-- {
		if bytes.HasPrefix(key, buf[len(buf)-k:]) {
			return k
		}
	}
	return 0
}

// masker 对整段内容按 key 精确值遮盖。
type masker struct {
	key  []byte
	mask []byte
}

func newMasker(key string) masker {
	kb, mask := []byte(key), []byte(MaskText)
	if markerCanRecreateKey(kb, mask) {
		mask = fallbackMarker(kb)
	}
	return masker{key: kb, mask: mask}
}

func fallbackMarker(key []byte) []byte {
	const candidates = "*~!? "
	// key 中不存在的单字节隔断所有替换边界。
	// 合法 key 不含空白，所以至少空格可选。
	for _, candidate := range []byte(candidates) {
		if bytes.IndexByte(key, candidate) < 0 {
			return []byte{candidate}
		}
	}
	// 无效但非空的 key 也要遮盖，不能假定存在未使用的候选字节。
	// 候选多于两个，总有一个不同于 key 的首尾字节。重复次数大于
	// key 内该字节的最长连续段：标记不在 key 内，也无法在边界拼出 key。
	chosen := candidates[0]
	for _, candidate := range []byte(candidates) {
		if candidate != key[0] && candidate != key[len(key)-1] {
			chosen = candidate
			break
		}
	}
	longest, run := 0, 0
	for _, b := range key {
		if b == chosen {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return bytes.Repeat([]byte{chosen}, longest+1)
}

// markerCanRecreateKey 检查标记内部和两端是否可能重新拼出 key。
// key 跨越整个标记时也不安全；这时标记本身是 key 的子串。
func markerCanRecreateKey(key, marker []byte) bool {
	if len(key) == 0 {
		return false
	}
	if bytes.Contains(marker, key) || bytes.Contains(key, marker) {
		return true
	}
	bound := min(len(key)-1, len(marker))
	for n := 1; n <= bound; n++ {
		if bytes.Equal(key[len(key)-n:], marker[:n]) || bytes.Equal(key[:n], marker[len(marker)-n:]) {
			return true
		}
	}
	return false
}

func (m masker) bytes(b []byte) []byte {
	if len(m.key) == 0 {
		return b
	}
	return bytes.ReplaceAll(b, m.key, m.mask)
}

// boundedBytes 同时限制原始内容和遮盖结果。先计算长度，再分配结果。
// 扩张先用除法检查上限，不计算可能溢出的乘积。
func (m masker) boundedBytes(b []byte, limit int64) ([]byte, bool) {
	var matches int64
	if _, ok := m.boundedSize(int64(len(b)), func() int64 {
		if len(m.key) == 0 {
			return 0
		}
		matches = int64(bytes.Count(b, m.key))
		return matches
	}, limit); !ok {
		return nil, false
	}
	if matches == 0 {
		return b, true
	}
	return m.bytes(b), true
}

// boundedStringSize 只计算遮盖后的长度，不分配遮盖文本。
func (m masker) boundedStringSize(s string, limit int64) (int64, bool) {
	return m.boundedSize(int64(len(s)), func() int64 {
		if len(m.key) == 0 {
			return 0
		}
		return int64(strings.Count(s, string(m.key)))
	}, limit)
}

func (m masker) boundedSize(inputLen int64, count func() int64, limit int64) (int64, bool) {
	limit = min(limit, int64(int(^uint(0)>>1)))
	if inputLen > limit {
		return 0, false
	}
	matches := count()
	if matches == 0 {
		return inputLen, true
	}
	growth := int64(len(m.mask)) - int64(len(m.key))
	if growth > 0 && matches > (limit-inputLen)/growth {
		return 0, false
	}
	// 正增长已证明 inputLen + matches*growth <= limit；负增长的绝对值
	// 不超过 inputLen，因为匹配不重叠。ReplaceAll 的长度计算不会溢出。
	return inputLen + matches*growth, true
}

func (m masker) str(s string) string {
	if len(m.key) == 0 {
		return s
	}
	return string(bytes.ReplaceAll([]byte(s), m.key, m.mask))
}

func (m masker) writer(w io.Writer) *maskWriter { return newMaskWriter(w, m.key, m.mask) }
