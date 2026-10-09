package expr

import (
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`D.exit_code == 0 && D.status == "ok"`,
		`x in ["a", "b", 1]`,
		`!(a || b) && c`,
		`"é😀" != s`,
		`((((x))))`,
		`x >= -1.5`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		e, err := Parse(src)
		if err != nil {
			c, ok := errcode.CodeOf(err)
			if !ok || (c != errcode.ExprSyntax && c != errcode.ExprLimit) {
				t.Fatalf("Parse(%q) returned unexpected error %v", src, err)
			}
			if e != nil {
				t.Fatalf("non-nil Expr with error")
			}
			return
		}
		if e == nil || e.root == nil {
			t.Fatalf("Parse(%q) returned empty Expr without error", src)
		}
		// 解析成功的输入，Paths 和 Check 也不能 panic
		_ = e.Paths()
		_ = Check(e, Schema{"a": TBool, "x": TNumber})
	})
}
