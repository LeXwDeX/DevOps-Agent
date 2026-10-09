package request

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// Scope 是申请的作用范围。Targets 是稳定 ID，排序去重（Normalize）。
type Scope struct {
	Project     string   `json:"project"`
	Environment string   `json:"environment"`
	Targets     []string `json:"targets"`
}

// Normalize 返回目标排序去重后的副本，不改原值。
func (s Scope) Normalize() Scope {
	s.Targets = sortedUnique(s.Targets)
	return s
}

// sortedUnique 返回排序去重后的新切片（永不为 nil）。
func sortedUnique(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return slices.Compact(out)
}

// sortedCopy 返回排序后的新切片，保留重复（永不为 nil）。
func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// Criterion 是一项成功判据：条件表达式或文字，二选一（61 第 3.3 节）。
// Expr 作用在执行结果的结构化输出上，不加前缀。写不成表达式的判据留作 Text，结果对比交给人工。
type Criterion struct {
	Expr string `json:"expr,omitempty"`
	Text string `json:"text,omitempty"`
}

// ValueRange 是预期变更的可选结构化范围，例如影响行数区间。
type ValueRange struct {
	Field string   `json:"field"`
	Min   *float64 `json:"min,omitempty"`
	Max   *float64 `json:"max,omitempty"`
}

// ExpectedChange 是预期变更：文字加可选的结构化范围。
type ExpectedChange struct {
	Text   string       `json:"text"`
	Ranges []ValueRange `json:"ranges,omitempty"`
}

// Expectation 是执行预期（61 第 3.2 节）。
type Expectation struct {
	Intent          string               `json:"intent"`
	Targets         []string             `json:"targets"`
	Category        consts.Category      `json:"category"`
	ExpectedChange  ExpectedChange       `json:"expected_change"`
	ExpectedOutput  string               `json:"expected_output"`
	SuccessCriteria []Criterion          `json:"success_criteria"`
	Reversible      consts.Reversibility `json:"reversible"`
	Rollback        string               `json:"rollback,omitempty"`
	HasPreview      bool                 `json:"has_preview"` // 单元是否声明原生预演；结果记在 Checks.Preview
	RiskNote        string               `json:"risk_note,omitempty"`
}

// Validate 检查必填字段、判据、回退方法、目标集合与范围一致、枚举合法。
// 不合格时返回 core.expectation_invalid，消息列出全部问题。
func (e Expectation) Validate(scope Scope) error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if strings.TrimSpace(e.Intent) == "" {
		add("intent is required")
	}
	if len(e.Targets) == 0 {
		add("targets is required")
	}
	switch {
	case e.Category == "":
		add("category is required")
	case !e.Category.Valid():
		add("unknown category %q", e.Category)
	}
	if strings.TrimSpace(e.ExpectedChange.Text) == "" {
		add("expected_change is required")
	}
	if strings.TrimSpace(e.ExpectedOutput) == "" {
		add("expected_output is required")
	}
	if len(e.SuccessCriteria) == 0 {
		add("at least one success criterion is required")
	}
	for i, c := range e.SuccessCriteria {
		hasExpr, hasText := strings.TrimSpace(c.Expr) != "", strings.TrimSpace(c.Text) != ""
		if hasExpr == hasText {
			add("success criterion #%d must set exactly one of expr or text", i)
		}
	}
	switch {
	case e.Reversible == "":
		add("reversible is required")
	case !e.Reversible.Valid():
		add("unknown reversible %q", e.Reversible)
	case e.Reversible == consts.Reversible && strings.TrimSpace(e.Rollback) == "":
		add("rollback is required when reversible")
	}
	if !slices.Equal(sortedUnique(e.Targets), sortedUnique(scope.Targets)) {
		add("targets %v differ from scope targets %v", sortedUnique(e.Targets), sortedUnique(scope.Targets))
	}
	if len(problems) > 0 {
		return errcode.New(errcode.ExpectationInvalid, strings.Join(problems, "; "))
	}
	return nil
}
