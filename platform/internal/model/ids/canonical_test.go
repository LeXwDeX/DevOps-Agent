package ids

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

func canon(t *testing.T, v any) string {
	t.Helper()
	b, err := Canonicalize(v)
	if err != nil {
		t.Fatalf("Canonicalize(%v): %v", v, err)
	}
	return string(b)
}

func TestCanonicalizeBasics(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, `null`},
		{true, `true`},
		{"a<b>&c", `"a<b>&c"`},
		{"\u2028\n\"\\", `"\u2028\n\"\\"`},
		{[]int{3, 1, 2}, `[3,1,2]`},
		{map[string]any{"b": 1, "a": []any{map[string]any{"z": 1, "y": nil}}}, `{"a":[{"y":null,"z":1}],"b":1}`},
		{map[string]int{}, `{}`},
		{[]byte("hi"), `"aGk="`},
		{json.RawMessage(` { "k" : [ 1 , 2 ] } `), `{"k":[1,2]}`},
	}
	for _, c := range cases {
		if got := canon(t, c.in); got != c.want {
			t.Errorf("Canonicalize(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

type orderA struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Tags  []string
}
type orderB struct {
	Tags  []string
	Count int    `json:"count"`
	Name  string `json:"name"`
}

func TestKeyOrderInsensitive(t *testing.T) {
	a := orderA{"x", 2, []string{"p", "q"}}
	b := orderB{[]string{"p", "q"}, 2, "x"}
	m := map[string]any{"name": "x", "count": 2, "Tags": []string{"p", "q"}}
	da, _ := DigestOf(a)
	db, _ := DigestOf(b)
	dm, _ := DigestOf(m)
	if da != db || da != dm {
		t.Errorf("digests differ: %s %s %s", da, db, dm)
	}
}

func TestAnyFieldChangeChangesDigest(t *testing.T) {
	base := map[string]any{
		"call": "c1", "rev": 1, "params": map[string]any{"a": "1", "b": []any{1, 2}},
		"keys": []string{"K1"}, "flag": true,
	}
	d0, _ := DigestOf(base)
	mutations := map[string]func(m map[string]any){
		"call":      func(m map[string]any) { m["call"] = "c2" },
		"rev":       func(m map[string]any) { m["rev"] = 2 },
		"nested":    func(m map[string]any) { m["params"].(map[string]any)["a"] = "2" },
		"list item": func(m map[string]any) { m["params"].(map[string]any)["b"] = []any{1, 3} },
		"list ord":  func(m map[string]any) { m["params"].(map[string]any)["b"] = []any{2, 1} },
		"extra":     func(m map[string]any) { m["extra"] = nil },
		"flag":      func(m map[string]any) { m["flag"] = false },
		"keys":      func(m map[string]any) { m["keys"] = []string{} },
	}
	for name, mut := range mutations {
		// 每次重新构造，避免共享嵌套 map
		c := map[string]any{
			"call": "c1", "rev": 1, "params": map[string]any{"a": "1", "b": []any{1, 2}},
			"keys": []string{"K1"}, "flag": true,
		}
		mut(c)
		d, err := DigestOf(c)
		if err != nil {
			t.Fatal(err)
		}
		if d == d0 {
			t.Errorf("mutation %q did not change digest", name)
		}
	}
}

func TestNumberNormalization(t *testing.T) {
	cases := map[string]string{
		"0": "0", "-0": "0", "0.0": "0", "-0.000e5": "0", "0e99999999999": "0",
		"1": "1", "1.0": "1", "1e0": "1", "1E0": "1", "10e-1": "1", "0.1e1": "1",
		"1.50": "1.5", "-1.50": "-1.5", "100": "100", "1e2": "100", "1.5e2": "150",
		"123.456": "123.456", "0.001": "0.001", "0.000001": "0.000001", "0.0000001": "1e-7",
		"1e-7": "1e-7", "12e-8": "1.2e-7",
		"1e20": "100000000000000000000", "1e21": "1e+21", "1.5e+30": "1.5e+30",
		"9007199254740993": "9007199254740993", "0.1": "0.1", "1e+0": "1",
		"123456789012345678901234567890": "1.2345678901234567890123456789e+29",
		"1e99999999999999999999":         "1e+99999999999999999999",
	}
	for in, want := range cases {
		got, err := normalizeNumber(in)
		if err != nil || got != want {
			t.Errorf("normalizeNumber(%q) = %q, %v; want %q", in, got, err, want)
		}
		// 幂等
		again, err := normalizeNumber(got)
		if err != nil || again != got {
			t.Errorf("not idempotent: %q -> %q -> %q", in, got, again)
		}
	}
	// 经 Canonicalize：整数、浮点、json.Number 得到同一结果
	vals := []any{1, 1.0, float64(1), json.Number("1.0"), json.Number("1e0"), int64(1), uint8(1)}
	for _, v := range vals {
		if got := canon(t, v); got != "1" {
			t.Errorf("Canonicalize(%#v) = %s", v, got)
		}
	}
}

func TestUnsupported(t *testing.T) {
	bad := []any{
		math.NaN(), math.Inf(1), math.Inf(-1),
		make(chan int), func() {}, map[string]any{"x": math.NaN()},
		json.Number("abc"), complex(1, 2),
	}
	for _, v := range bad {
		_, err := Canonicalize(v)
		if code, ok := errcode.CodeOf(err); !ok || code != errcode.CanonicalUnsupported {
			t.Errorf("Canonicalize(%T) err = %v", v, err)
		}
		if _, err := DigestOf(v); !errors.Is(err, errcode.New(errcode.CanonicalUnsupported, "")) {
			t.Errorf("DigestOf(%T) err = %v", v, err)
		}
	}
}

func TestDigestKnownValue(t *testing.T) {
	// {"a":1} 的 SHA-256
	d, _ := DigestOf(map[string]int{"a": 1})
	want := DigestOfBytes([]byte(`{"a":1}`))
	if d != want {
		t.Errorf("digest = %s, want %s", d, want)
	}
	if string(want) != "015abd7f5cc57a2dd94b7590f04ad8084273905ee33ec5cebeae62276a97f862" {
		t.Errorf("sha256 mismatch: %s", want)
	}
}

func parseJSON(data []byte) (any, bool) {
	if !json.Valid(data) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

func FuzzCanonicalize(f *testing.F) {
	for _, s := range []string{
		`null`, `{"b":1,"a":[1,2.50,1e0]}`, `"a<b>&\u2028"`, `[]`, `-0.0e5`, `{"a":{"b":{"c":[true,false,null]}}}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		v, ok := parseJSON(data)
		if !ok {
			return
		}
		out, err := Canonicalize(v)
		if err != nil {
			t.Fatalf("Canonicalize failed on valid JSON %q: %v", data, err)
		}
		if !json.Valid(out) {
			t.Fatalf("output not valid JSON: %q", out)
		}
		v2, ok := parseJSON(out)
		if !ok {
			t.Fatalf("cannot reparse %q", out)
		}
		out2, err := Canonicalize(v2)
		if err != nil || !bytes.Equal(out, out2) {
			t.Fatalf("not idempotent: %q -> %q (%v)", out, out2, err)
		}
		d1, _ := DigestOf(v)
		d2, _ := DigestOf(v2)
		if d1 != d2 {
			t.Fatalf("digests differ")
		}
	})
}
