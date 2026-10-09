package archtest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	modulePath   = "github.com/LeXwDeX/DevOps-Agent/platform"
	internalPath = modulePath + "/internal/"
)

// allowedLayers 给出每一层可以导入的内部层（标准库总是允许）。
var allowedLayers = map[string][]string{
	"schema": {"schema"},
	"model":  {"schema", "model"},
	"core":   {"schema", "model", "core"},
}

// isStdlib：标准库的导入路径第一段不含点。
func isStdlib(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

// layerOf 返回内部包导入路径所属的层（internal/ 下第一段）；不是本模块 internal 包返回 ""。
func layerOf(path string) string {
	rest, ok := strings.CutPrefix(path, internalPath)
	if !ok {
		return ""
	}
	layer, _, _ := strings.Cut(rest, "/")
	return layer
}

// violation 判断位于 internal/<layer>/<pkgDir> 的文件导入 imp 是否违规，返回原因；合规返回 ""。
// isTest 表示该文件是 _test.go。
func violation(layer, pkgDir string, isTest bool, imp string) string {
	if isStdlib(imp) {
		return ""
	}
	target := layerOf(imp)
	if target == "" {
		return "third-party or non-internal import"
	}
	if target == "provider" || target == "module" {
		return "imports internal/" + target
	}
	ok := false
	for _, l := range allowedLayers[layer] {
		if l == target {
			ok = true
		}
	}
	if !ok {
		return layer + " must not import internal/" + target
	}
	// coretest 只允许自己和 _test.go 导入。
	if strings.HasPrefix(imp, internalPath+"core/coretest") && pkgDir != "core/coretest" && !isTest {
		return "non-test code imports core/coretest"
	}
	return ""
}

func TestDependencyDirection(t *testing.T) {
	fset := token.NewFileSet()
	files := 0
	for layer := range allowedLayers {
		root := filepath.Join("..", layer)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				t.Errorf("parse %s: %v", path, err)
				return nil
			}
			files++
			rel, _ := filepath.Rel("..", filepath.Dir(path))
			rel = filepath.ToSlash(rel)
			isTest := strings.HasSuffix(path, "_test.go")
			for _, spec := range f.Imports {
				imp, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Errorf("%s: bad import %s", path, spec.Path.Value)
					continue
				}
				if why := violation(layer, rel, isTest, imp); why != "" {
					t.Errorf("%s imports %q: %s", filepath.ToSlash(path), imp, why)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if files < 20 {
		t.Fatalf("only %d Go files scanned; walk is broken", files)
	}
}

// TestViolationRules 保证规则本身会报错：没有它，规则写错也会一直"通过"。
func TestViolationRules(t *testing.T) {
	in := func(p string) string { return internalPath + p }
	cases := []struct {
		layer, dir string
		test       bool
		imp        string
		bad        bool
	}{
		{"schema", "schema/errcode", false, "fmt", false},
		{"schema", "schema/consts", false, in("schema/errcode"), false},
		{"schema", "schema/consts", false, in("model/ids"), true},
		{"schema", "schema/consts", true, in("core/events"), true},
		{"model", "model/request", false, in("schema/consts"), false},
		{"model", "model/request", false, in("model/ids"), false},
		{"model", "model/request", false, in("core/policy"), true},
		{"core", "core/pipeline", false, in("core/ports"), false},
		{"core", "core/pipeline", false, in("model/request"), false},
		{"core", "core/pipeline", false, in("provider/dcg"), true},
		{"core", "core/pipeline", true, in("provider/dcg"), true},
		{"core", "core/pipeline", false, in("module/session"), true},
		{"core", "core/pipeline", false, "github.com/google/uuid", true},
		{"core", "core/pipeline", true, "github.com/stretchr/testify/require", true},
		{"core", "core/pipeline", false, modulePath + "/cmd/x", true},
		{"core", "core/pipeline", false, in("core/coretest"), true},
		{"core", "core/pipeline", true, in("core/coretest"), false},
		{"core", "core/coretest", false, in("core/ports"), false},
		{"core", "core/coretest", false, in("core/coretest"), false},
	}
	for _, c := range cases {
		got := violation(c.layer, c.dir, c.test, c.imp)
		if (got != "") != c.bad {
			t.Errorf("%s %s test=%v imports %s: violation=%q, want bad=%v", c.layer, c.dir, c.test, c.imp, got, c.bad)
		}
	}
	for _, std := range []string{"fmt", "encoding/json", "net/http", "crypto/sha256"} {
		if !isStdlib(std) {
			t.Errorf("%s should be stdlib", std)
		}
	}
	if isStdlib("golang.org/x/sys") || isStdlib("github.com/x/y") {
		t.Error("third-party treated as stdlib")
	}
}
