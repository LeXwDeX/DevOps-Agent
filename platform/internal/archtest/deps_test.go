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

// coreForbiddenStdlib 是 core 非测试文件不得导入的标准库包：core 没有外部 I/O，文件、网络、进程、系统调用
// 都只能经 ports 里的端口（61 第 10 节审核清单第 3 项）。带 "/" 结尾的项匹配整个子树。
var coreForbiddenStdlib = []string{"os", "os/", "net", "net/", "syscall", "syscall/", "unsafe", "C"}

// forbiddenStdlib 判断标准库导入是否在 core 的黑名单里。
func forbiddenStdlib(imp string) bool {
	for _, f := range coreForbiddenStdlib {
		if imp == f || (strings.HasSuffix(f, "/") && strings.HasPrefix(imp, f)) {
			return true
		}
	}
	return false
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
		// 只限制 core 的非测试文件；测试文件可以用 os 等（例如读文件）。
		if layer == "core" && !isTest && forbiddenStdlib(imp) {
			return "core must not import " + imp + " (no external I/O; use a port)"
		}
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
		// core 非测试文件的标准库黑名单：外部 I/O、系统调用、不安全指针。
		{"core", "core/pipeline", false, "os", true},
		{"core", "core/pipeline", false, "os/exec", true},
		{"core", "core/pipeline", false, "os/signal", true},
		{"core", "core/pipeline", false, "net", true},
		{"core", "core/pipeline", false, "net/http", true},
		{"core", "core/pipeline", false, "net/url", true},
		{"core", "core/pipeline", false, "syscall", true},
		{"core", "core/pipeline", false, "unsafe", true},
		{"core", "core/pipeline", false, "C", true},
		{"core", "core/coretest", false, "os", true},
		// 测试文件和别的层不受这条限制；名字相近的包不误报。
		{"core", "core/pipeline", true, "os", false},
		{"core", "core/pipeline", true, "net/http/httptest", false},
		{"model", "model/ids", false, "os", false},
		{"schema", "schema/errcode", false, "net", false},
		{"core", "core/pipeline", false, "context", false},
		{"core", "core/pipeline", false, "io", false},
		{"core", "core/pipeline", false, "io/fs", false},
		{"core", "core/pipeline", false, "sync/atomic", false},
		{"core", "core/pipeline", false, "strings", false},
		{"core", "core/pipeline", false, "reflect", false},
		{"core", "core/coretest", false, "sync", false},
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
