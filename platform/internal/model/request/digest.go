package request

import (
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
)

// 摘要用专门的结构体：字段名固定，不随存储结构的 json 标签变化。
// 集合类字段（目标、K 列表）先排序副本再取摘要，所以对顺序不敏感。
// nil 与空集合等价。规范化与 SHA-256 在 ids.DigestOf（61 第 7.2 节）。

type digestUnit struct {
	ID      string     `json:"id"`
	Version int64      `json:"version"`
	Digest  ids.Digest `json:"digest"`
}

type digestContent struct {
	Unit       *digestUnit    `json:"unit"`
	Rendered   string         `json:"rendered"`
	Params     map[string]any `json:"params"`
	SecretKeys []string       `json:"secret_keys"`
}

type digestScope struct {
	Project     string   `json:"project"`
	Environment string   `json:"environment"`
	Targets     []string `json:"targets"`
}

type digestRange struct {
	Field string   `json:"field"`
	Min   *float64 `json:"min"`
	Max   *float64 `json:"max"`
}

type digestCriterion struct {
	Expr string `json:"expr"`
	Text string `json:"text"`
}

type digestExpectation struct {
	Intent          string            `json:"intent"`
	Targets         []string          `json:"targets"`
	Category        string            `json:"category"`
	ChangeText      string            `json:"change_text"`
	ChangeRanges    []digestRange     `json:"change_ranges"`
	ExpectedOutput  string            `json:"expected_output"`
	SuccessCriteria []digestCriterion `json:"success_criteria"`
	Reversible      string            `json:"reversible"`
	Rollback        string            `json:"rollback"`
	HasPreview      bool              `json:"has_preview"`
	RiskNote        string            `json:"risk_note"`
}

type digestContentSet struct {
	Content     digestContent     `json:"content"`
	Scope       digestScope       `json:"scope"`
	Expectation digestExpectation `json:"expectation"`
}

type digestFreeze struct {
	Content          digestContent     `json:"content"`
	Scope            digestScope       `json:"scope"`
	Expectation      digestExpectation `json:"expectation"`
	FingerprintValue string            `json:"fingerprint_value"`
	Policy           *policy.Decision  `json:"policy"`
	Revision         ids.Revision      `json:"revision"`
}

func viewContent(c Content) digestContent {
	v := digestContent{
		Rendered:   c.Rendered,
		Params:     CloneMap(c.Params),
		SecretKeys: sortedCopy(c.SecretKeys),
	}
	if v.Params == nil {
		v.Params = map[string]any{}
	}
	if c.Unit != nil {
		u := digestUnit(*c.Unit)
		v.Unit = &u
	}
	return v
}

func viewScope(s Scope) digestScope {
	return digestScope{Project: s.Project, Environment: s.Environment, Targets: sortedUnique(s.Targets)}
}

func viewExpectation(e Expectation) digestExpectation {
	v := digestExpectation{
		Intent:          e.Intent,
		Targets:         sortedUnique(e.Targets),
		Category:        string(e.Category),
		ChangeText:      e.ExpectedChange.Text,
		ChangeRanges:    []digestRange{},
		ExpectedOutput:  e.ExpectedOutput,
		SuccessCriteria: []digestCriterion{},
		Reversible:      string(e.Reversible),
		Rollback:        e.Rollback,
		HasPreview:      e.HasPreview,
		RiskNote:        e.RiskNote,
	}
	for _, r := range e.ExpectedChange.Ranges {
		v.ChangeRanges = append(v.ChangeRanges, digestRange(r))
	}
	for _, c := range e.SuccessCriteria {
		v.SuccessCriteria = append(v.SuccessCriteria, digestCriterion(c))
	}
	return v
}

// ContentDigest 返回当前内容的摘要，覆盖 Content、Scope、Expectation（61 第 3.1 节）。
// 对目标和 K 列表的顺序不敏感。
func (r *ExecutionRequest) ContentDigest() (ids.Digest, error) {
	return ids.DigestOf(digestContentSet{
		Content:     viewContent(r.Content),
		Scope:       viewScope(r.Scope),
		Expectation: viewExpectation(r.Expectation),
	})
}

// FreezeDigest 返回冻结摘要，覆盖 61 第 7.2 节列出的字段：
// Content（含 Params 和 SecretKeys）、Scope、Expectation、指纹值、策略判定、Revision。
// 其他字段（人工记录、时间戳、检验中的其他结果）不进入摘要。
func (r *ExecutionRequest) FreezeDigest() (ids.Digest, error) {
	f := digestFreeze{
		Content:     viewContent(r.Content),
		Scope:       viewScope(r.Scope),
		Expectation: viewExpectation(r.Expectation),
		Revision:    r.Revision,
	}
	if r.Fingerprint != nil {
		f.FingerprintValue = r.Fingerprint.Value
	}
	if r.Checks.Policy != nil {
		d := r.Checks.Policy.Clone()
		f.Policy = &d
	}
	return ids.DigestOf(f)
}
