package ids

import (
	"bytes"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

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
//   - 任何字符串（含对象键）不是合法 UTF-8，返回 core.canonical_unsupported。
//     encoding/json 会把非法字节悄悄换成 U+FFFD，不同的非法字节会得到同一个摘要，所以必须拒绝（K-13、K-15）。

// Canonicalize 返回 v 的规范化 JSON。
func Canonicalize(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, errcode.Wrap(errcode.CanonicalUnsupported, "marshal", err)
	}
	if err := checkUTF8(v, raw); err != nil {
		return nil, err
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

// checkUTF8 检查 v 里所有会被写进 JSON 的字符串（含对象键）都是合法 UTF-8。
// encoding/json 会把非法字节悄悄换成 U+FFFD，输出里分不出来，所以要在序列化之前沿着值本身检查。
// raw 是 json.Marshal 的输出：自定义 MarshalJSON 原样透传的字节在这里再查一遍。
func checkUTF8(v any, raw []byte) error {
	if !utf8.Valid(raw) || !validStrings(reflect.ValueOf(v)) {
		return errcode.New(errcode.CanonicalUnsupported, "string is not valid UTF-8")
	}
	return nil
}

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

// validStrings 沿着 reflect 值走一遍，所有字符串和字符串键都必须是合法 UTF-8。
// 实现了 json.Marshaler 的值由 Marshal 输出的 UTF-8 检查覆盖；实现了 TextMarshaler 的值检查它的文本。
func validStrings(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	t := v.Type()
	if t.Implements(jsonMarshalerType) {
		return true
	}
	if t.Implements(textMarshalerType) && (v.Kind() != reflect.Pointer || !v.IsNil()) {
		text, err := v.Interface().(encoding.TextMarshaler).MarshalText()
		return err != nil || utf8.Valid(text)
	}
	switch v.Kind() {
	case reflect.String:
		return utf8.ValidString(v.String())
	case reflect.Pointer, reflect.Interface:
		return v.IsNil() || validStrings(v.Elem())
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			return true // []byte 编码为 base64
		}
		for i := 0; i < v.Len(); i++ {
			if !validStrings(v.Index(i)) {
				return false
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if !validStrings(iter.Key()) || !validStrings(iter.Value()) {
				return false
			}
		}
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() || f.Tag.Get("json") == "-" {
				continue
			}
			if !validStrings(v.Field(i)) {
				return false
			}
		}
	}
	return true
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
