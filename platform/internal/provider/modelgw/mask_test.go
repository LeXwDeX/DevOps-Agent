package modelgw

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
)

type chunkRecorder struct {
	buf    bytes.Buffer
	writes int
}

func (c *chunkRecorder) Write(p []byte) (int, error) {
	c.writes++
	return c.buf.Write(p)
}

// streamMask 按 sizes 切块写入 maskWriter，返回输出。
func streamMask(full, key string, sizes []int) string {
	rec := &chunkRecorder{}
	mw := newMasker(key).writer(rec)
	rest := []byte(full)
	for i := 0; len(rest) > 0; i++ {
		n := 1
		if len(sizes) > 0 {
			n = sizes[i%len(sizes)]
		}
		if n < 1 {
			n = 1
		}
		if n > len(rest) {
			n = len(rest)
		}
		if _, err := mw.Write(rest[:n]); err != nil {
			panic(err)
		}
		rest = rest[n:]
	}
	if err := mw.Close(); err != nil {
		panic(err)
	}
	return rec.buf.String()
}

func TestMaskWriterKeySplit(t *testing.T) {
	key := "SECRETKEY"
	full := "aaSECRETKEYbb SECRETSECRETKEY SECRETKE"
	want := strings.ReplaceAll(full, key, MaskText)
	for size := 1; size <= len(full); size++ {
		if got := streamMask(full, key, []int{size}); got != want {
			t.Fatalf("size %d: got %q want %q", size, got, want)
		}
	}
}

func TestMaskWriterHoldbackBound(t *testing.T) {
	key := "SECRETKEY"
	rec := &chunkRecorder{}
	mw := newMaskWriter(rec, []byte(key), []byte(MaskText))
	// 没有可疑前缀时立即输出
	_, _ = mw.Write([]byte("hello world"))
	if rec.buf.String() != "hello world" {
		t.Fatalf("clean data should pass at once, got %q", rec.buf.String())
	}
	// 以 key 前缀结尾时留下，且不超过 len(key)-1
	_, _ = mw.Write([]byte(" SECRETKE"))
	if len(mw.held) != len("SECRETKE") || len(mw.held) > len(key)-1 {
		t.Fatalf("held=%q", mw.held)
	}
	_ = mw.Close()
	if rec.buf.String() != "hello world SECRETKE" {
		t.Fatalf("got %q", rec.buf.String())
	}
}

func TestMaskWriterOverlappingKey(t *testing.T) {
	// key 自身有重叠前缀时，与 ReplaceAll 的最左不重叠语义一致
	for _, key := range []string{"aa", "aba", "aab", "abab"} {
		for _, full := range []string{"aaaaa", "ababababa", "aabaab", "aaab", "xabaabay"} {
			want := strings.ReplaceAll(full, key, MaskText)
			for size := 1; size <= len(full); size++ {
				if got := streamMask(full, key, []int{size}); got != want {
					t.Fatalf("key=%q full=%q size=%d got %q want %q", key, full, size, got, want)
				}
			}
		}
	}
}

func TestMaskWriterRandomized(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 3000; i++ {
		key, full, sizes := randomCase(rng)
		want := strings.ReplaceAll(full, key, MaskText)
		if got := streamMask(full, key, sizes); got != want {
			t.Fatalf("key=%q full=%q sizes=%v\ngot  %q\nwant %q", key, full, sizes, got, want)
		}
	}
}

func randomCase(rng *rand.Rand) (key, full string, sizes []int) {
	const alpha = "abc"
	kb := make([]byte, 1+rng.Intn(6))
	for i := range kb {
		kb[i] = alpha[rng.Intn(len(alpha))]
	}
	key = string(kb)
	var sb strings.Builder
	for n := rng.Intn(40); n > 0; n-- {
		if rng.Intn(4) == 0 {
			sb.WriteString(key)
		} else {
			sb.WriteByte(alpha[rng.Intn(len(alpha))])
		}
	}
	full = sb.String()
	for n := 1 + rng.Intn(5); n > 0; n-- {
		sizes = append(sizes, 1+rng.Intn(8))
	}
	return
}

func TestMaskWriterEmptyKeyPassThrough(t *testing.T) {
	rec := &chunkRecorder{}
	mw := newMaskWriter(rec, nil, []byte(MaskText))
	_, _ = mw.Write([]byte("abc"))
	_ = mw.Close()
	if rec.buf.String() != "abc" {
		t.Fatal(rec.buf.String())
	}
}

// buildFuzzInput 把 key 按 ins 给出的位置插入 data。
func buildFuzzInput(data []byte, key string, ins []byte) string {
	out := append([]byte(nil), data...)
	for _, p := range ins {
		pos := 0
		if len(out) > 0 {
			pos = int(p) % (len(out) + 1)
		}
		out = append(out[:pos], append([]byte(key), out[pos:]...)...)
	}
	return string(out)
}

func FuzzStreamMask(f *testing.F) {
	f.Add([]byte("data: hello\n\n"), "SECRET", []byte{3, 9}, []byte{1, 2, 3})
	f.Add([]byte("xxSECRETyy"), "SECRET", []byte{}, []byte{1})
	f.Add([]byte("aaaaaaaa"), "aa", []byte{0, 4}, []byte{2, 1})
	f.Add([]byte(""), "k", []byte{0, 0, 0}, []byte{5})
	f.Add([]byte("bb["), "b[", []byte{}, []byte{1})
	f.Add([]byte("]bb"), "]b", []byte{}, []byte{1, 2})
	f.Add([]byte("*~!?b["), "*~!?b[", []byte{0}, []byte{1})
	f.Add([]byte("prefix suffix"), "*~!? "+MaskText, []byte{3}, []byte{1, 4})
	f.Fuzz(func(t *testing.T, data []byte, key string, ins []byte, sizes []byte) {
		if len(key) == 0 || len(key) > 32 || len(ins) > 16 {
			t.Skip()
		}
		full := buildFuzzInput(data, key, ins)
		ns := make([]int, len(sizes))
		for i, s := range sizes {
			ns[i] = int(s)%17 + 1
		}
		want := newMasker(key).str(full)
		got := streamMask(full, key, ns)
		if got != want {
			t.Fatalf("key=%q sizes=%v\ngot  %q\nwant %q", key, ns, got, want)
		}
		if strings.Contains(got, key) {
			t.Fatal("mask replacement recreated the key")
		}
	})
}
