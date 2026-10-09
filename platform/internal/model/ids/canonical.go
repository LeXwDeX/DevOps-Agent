package ids

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"sort"
	"strings"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// 规范化规则（61 第 7.2 节：字段排序后的 JSON，再取 SHA-256）：
//   - 先按 encoding/json 的规则序列化，再解码为通用结构后重新编码。
//   - 对象键按解码后的字节序排列；无多余空白；不做 HTML 转义。
//   - 数组保持原顺序。集合类内容由调用方先自行排序。
//   - 字符串用标准 JSON 转义（U+2028、U+2029 也转义）。
//   - 数字不经过浮点，按十进制文本规范化：去掉符号 +、前导零和小数尾零，
//     -0 记为 0。有效数字为 d.ddd，指数 x 满足 -6 <= x < 21 时写成普通小数，
//     否则写成 d.ddde+X / d.ddde-X。所以 1、1.0、1e0 都得到 1。
//   - NaN、Inf 和无法序列化的类型返回 core.canonical_unsupported。

// Canonicalize 返回 v 的规范化 JSON。
func Canonicalize(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, errcode.Wrap(errcode.CanonicalUnsupported, "marshal", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, errcode.Wrap(errcode.CanonicalUnsupported, "decode", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errcode.New(errcode.CanonicalUnsupported, "trailing data")
	}
	var buf bytes.Buffer
	if err := encode(&buf, tree); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DigestOf 返回 v 规范化 JSON 的 SHA-256。
func DigestOf(v any) (Digest, error) {
	b, err := Canonicalize(v)
	if err != nil {
		return "", err
	}
	return DigestOfBytes(b), nil
}

// DigestOfBytes 返回字节串的 SHA-256。
func DigestOfBytes(b []byte) Digest {
	sum := sha256.Sum256(b)
	return Digest(hex.EncodeToString(sum[:]))
}

func encode(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		encodeString(buf, x)
	case json.Number:
		s, err := normalizeNumber(string(x))
		if err != nil {
			return err
		}
		buf.WriteString(s)
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encode(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			encodeString(buf, k)
			buf.WriteByte(':')
			if err := encode(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return errcode.Newf(errcode.CanonicalUnsupported, "unsupported type %T", v)
	}
	return nil
}

func encodeString(buf *bytes.Buffer, s string) {
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // string 编码不会失败
	buf.Write(bytes.TrimSuffix(tmp.Bytes(), []byte("\n")))
}

// normalizeNumber 按上面的规则把 JSON 数字文本规范化。输入已是合法 JSON 数字。
func normalizeNumber(s string) (string, error) {
	bad := func() (string, error) {
		return "", errcode.Newf(errcode.CanonicalUnsupported, "bad number %q", s)
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	mant, expStr := s, ""
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mant, expStr = s[:i], s[i+1:]
	}
	intPart, frac := mant, ""
	if i := strings.IndexByte(mant, '.'); i >= 0 {
		intPart, frac = mant[:i], mant[i+1:]
	}
	if intPart == "" {
		return bad()
	}
	exp := new(big.Int)
	if expStr != "" {
		if _, ok := exp.SetString(expStr, 10); !ok {
			return bad()
		}
	}
	digits := intPart + frac
	point := len(intPart) // 小数点在 digits 的第 point 位之后
	lead := len(digits) - len(strings.TrimLeft(digits, "0"))
	digits = digits[lead:]
	point -= lead
	digits = strings.TrimRight(digits, "0")
	if digits == "" {
		return "0", nil
	}
	// 科学计数指数 x：值 = d.ddd × 10^x
	x := new(big.Int).Add(exp, big.NewInt(int64(point-1)))

	var out strings.Builder
	if neg {
		out.WriteByte('-')
	}
	if x.Cmp(big.NewInt(-6)) >= 0 && x.Cmp(big.NewInt(21)) < 0 {
		xi := int(x.Int64())
		switch {
		case xi < 0:
			out.WriteString("0.")
			out.WriteString(strings.Repeat("0", -xi-1))
			out.WriteString(digits)
		case len(digits) <= xi+1:
			out.WriteString(digits)
			out.WriteString(strings.Repeat("0", xi+1-len(digits)))
		default:
			out.WriteString(digits[:xi+1])
			out.WriteByte('.')
			out.WriteString(digits[xi+1:])
		}
		return out.String(), nil
	}
	out.WriteByte(digits[0])
	if len(digits) > 1 {
		out.WriteByte('.')
		out.WriteString(digits[1:])
	}
	out.WriteByte('e')
	if x.Sign() >= 0 {
		out.WriteByte('+')
	}
	out.WriteString(x.String())
	return out.String(), nil
}
