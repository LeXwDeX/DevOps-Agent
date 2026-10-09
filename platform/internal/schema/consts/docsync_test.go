package consts

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 文档枚举表与代码常量比对（52 第 6.2 节）。

const schemaDoc = "../../../../docs/50-engineering/52-schema-and-constants.md"

var backtick = regexp.MustCompile("`([^`]+)`")

// docValues 找到首列为 label 的表格行，取其第三列（建议取值）中的反引号值。
func docValues(t *testing.T, doc, label string) map[string]bool {
	t.Helper()
	for _, line := range strings.Split(doc, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		// cells[0] 为空；cells[1] 首列；cells[3] 建议取值
		if len(cells) < 5 || strings.TrimSpace(cells[1]) != label {
			continue
		}
		set := map[string]bool{}
		for _, m := range backtick.FindAllStringSubmatch(cells[3], -1) {
			set[m[1]] = true
		}
		return set
	}
	t.Fatalf("row %q not found in %s", label, schemaDoc)
	return nil
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestMatchesSchemaDoc(t *testing.T) {
	raw, err := os.ReadFile(schemaDoc)
	if err != nil {
		// 文档缺失不能让比对静默通过：路径变了就必须修这个测试。
		t.Fatalf("cannot read %s: %v", schemaDoc, err)
	}
	doc := string(raw)
	for _, c := range []struct {
		label  string
		all    []string
		ignore []string
	}{
		{"执行申请状态", strs(RequestState("").All()), nil},
		{"既定任务运行状态", strs(RunState("").All()), nil},
		{"策略模式", strs(PolicyMode("").All()), []string{"allow_auto"}},
		{"策略层级", strs(PolicyLayer("").All()), nil},
		{"权限区", strs(Zone("").All()), nil},
		{"会话模式", strs(SessionMode("").All()), nil},
		{"人工动作", strs(HumanAction("").All()), nil},
		{"修订来源", strs(RevisionSource("").All()), nil},
		{"自审结论", strs(SelfReviewVerdict("").All()), nil},
		{"申请来源", strs(RequestSource("").All()), nil},
		{"读写类别", strs(Category("").All()), nil},
		{"可逆性", strs(Reversibility("").All()), nil},
		{"停止原因", strs(StopReason("").All()), nil},
		{"拒绝来源", strs(RejectionSource("").All()), nil},
		{"dcg 结果", strs(DCGResult("").All()), nil},
		{"原生预演状态", strs(PreviewStatus("").All()), nil},
		{"指纹状态", strs(FingerprintStatus("").All()), nil},
	} {
		t.Run(c.label, func(t *testing.T) {
			got := docValues(t, doc, c.label)
			for _, ig := range c.ignore {
				delete(got, ig)
			}
			want := map[string]bool{}
			for _, v := range c.all {
				want[v] = true
			}
			g, w := strings.Join(sorted(got), ","), strings.Join(sorted(want), ",")
			if g != w {
				t.Errorf("doc = [%s]\ncode = [%s]", g, w)
			}
		})
	}
}
