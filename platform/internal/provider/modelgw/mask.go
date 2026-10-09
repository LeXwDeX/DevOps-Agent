package modelgw

import (
	"bytes"
	"io"
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
		if _, err := m.w.Write(out); err != nil {
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
	_, err := m.w.Write(b)
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
	return masker{key: []byte(key), mask: []byte(MaskText)}
}

func (m masker) bytes(b []byte) []byte {
	if len(m.key) == 0 {
		return b
	}
	return bytes.ReplaceAll(b, m.key, m.mask)
}

func (m masker) str(s string) string {
	if len(m.key) == 0 {
		return s
	}
	return string(bytes.ReplaceAll([]byte(s), m.key, m.mask))
}

func (m masker) writer(w io.Writer) *maskWriter { return newMaskWriter(w, m.key, m.mask) }
