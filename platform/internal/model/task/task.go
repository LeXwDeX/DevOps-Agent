// Package task 定义既定任务的定义、步骤、分支和运行记录（61 第 3、7.3、7.4 节）。
// 只有数据和结构校验；条件表达式的类型检查在 core/orchestrator。
package task

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// 字段类型。
const (
	KindString = "string"
	KindNumber = "number"
	KindBool   = "bool"
	KindList   = "list"
)

// FieldType 是参数或输出字段的类型。Elem 只在 Kind 为 list 时用，取标量类型。
type FieldType struct {
	Kind string `json:"kind"`
	Elem string `json:"elem,omitempty"`
}

func scalarKind(k string) bool { return k == KindString || k == KindNumber || k == KindBool }

// Valid 判断类型是否合法：标量不带 Elem；list 的 Elem 必须是标量。
func (t FieldType) Valid() bool {
	switch {
	case scalarKind(t.Kind):
		return t.Elem == ""
	case t.Kind == KindList:
		return scalarKind(t.Elem)
	}
	return false
}

// FieldSpec 描述一个参数或输出字段。
type FieldSpec struct {
	Name     string    `json:"name"`
	Type     FieldType `json:"type"`
	Required bool      `json:"required,omitempty"`
}

// Branch 是一条分支：条件成立则去 To。按顺序匹配，第一条成立的生效。
type Branch struct {
	When string `json:"when"`
	To   string `json:"to"`
}

// Step 是任务的一步。Next 与 Branches 至多设一个；都不设表示最后一步。
type Step struct {
	ID       string          `json:"id"`
	Unit     request.UnitRef `json:"unit"`
	Outputs  []FieldSpec     `json:"outputs,omitempty"`
	Next     string          `json:"next,omitempty"` // 无条件的下一步，空 = 结束
	Branches []Branch        `json:"branches,omitempty"`
}

// Definition 是已登记的任务定义。
type Definition struct {
	ID         string      `json:"id"`
	Version    int64       `json:"version"`
	Digest     ids.Digest  `json:"digest"`
	Params     []FieldSpec `json:"params,omitempty"`
	SecretKeys []string    `json:"secret_keys,omitempty"` // 需要的 K 列表，没有 V
	Entry      string      `json:"entry"`
	Steps      []Step      `json:"steps"`
}

// Step 按 ID 查找步骤。
func (d *Definition) Step(id string) (Step, bool) {
	for _, s := range d.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return Step{}, false
}

// Clone 深拷贝。
func (d *Definition) Clone() *Definition {
	if d == nil {
		return nil
	}
	c := *d
	c.Params = slices.Clone(d.Params)
	c.SecretKeys = slices.Clone(d.SecretKeys)
	if d.Steps != nil {
		c.Steps = make([]Step, len(d.Steps))
		for i, s := range d.Steps {
			s.Outputs = slices.Clone(s.Outputs)
			s.Branches = slices.Clone(s.Branches)
			c.Steps[i] = s
		}
	}
	return &c
}

func checkFields(what string, fields []FieldSpec, add func(string, ...any)) {
	seen := map[string]bool{}
	for _, f := range fields {
		switch {
		case f.Name == "":
			add("%s: empty field name", what)
		case seen[f.Name]:
			add("%s: duplicate field %q", what, f.Name)
		}
		seen[f.Name] = true
		if !f.Type.Valid() {
			add("%s: field %q has invalid type %+v", what, f.Name, f.Type)
		}
	}
}

// Validate 只检查结构：ID 唯一、入口存在、去向存在、Next 与 Branches 不并存、
// 字段类型合法、无环。表达式的类型检查由 core/orchestrator 完成。
// 不合格时返回 core.task_invalid，消息列出全部问题。
func (d *Definition) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if d.ID == "" {
		add("task id is required")
	}
	checkFields("params", d.Params, add)
	seenKey := map[string]bool{}
	for _, k := range d.SecretKeys {
		if k == "" {
			add("empty secret key")
		} else if seenKey[k] {
			add("duplicate secret key %q", k)
		}
		seenKey[k] = true
	}

	index := map[string]int{}
	for i, s := range d.Steps {
		if s.ID == "" {
			add("step #%d has empty id", i)
			continue
		}
		if _, dup := index[s.ID]; dup {
			add("duplicate step id %q", s.ID)
			continue
		}
		index[s.ID] = i
	}
	if len(d.Steps) == 0 {
		add("at least one step is required")
	}
	if _, ok := index[d.Entry]; !ok {
		add("entry %q is not a step", d.Entry)
	}

	edges := map[string][]string{}
	for _, s := range d.Steps {
		if s.Unit.ID == "" {
			add("step %q has no unit", s.ID)
		}
		checkFields("step "+s.ID+" outputs", s.Outputs, add)
		if s.Next != "" && len(s.Branches) > 0 {
			add("step %q sets both next and branches", s.ID)
		}
		if s.Next != "" {
			if _, ok := index[s.Next]; !ok {
				add("step %q next %q does not exist", s.ID, s.Next)
			}
			edges[s.ID] = append(edges[s.ID], s.Next)
		}
		for i, b := range s.Branches {
			if strings.TrimSpace(b.When) == "" {
				add("step %q branch #%d has empty condition", s.ID, i)
			}
			if _, ok := index[b.To]; !ok {
				add("step %q branch #%d target %q does not exist", s.ID, i, b.To)
			}
			edges[s.ID] = append(edges[s.ID], b.To)
		}
	}
	if cyc := findCycle(d.Steps, edges); cyc != "" {
		add("step graph has a cycle through %q", cyc)
	}

	if len(problems) > 0 {
		return errcode.New(errcode.TaskInvalid, strings.Join(problems, "; "))
	}
	return nil
}

// findCycle 用三色深度优先查环，返回环上的一个步骤 ID，无环返回空串。
func findCycle(steps []Step, edges map[string][]string) string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var visit func(id string) string
	visit = func(id string) string {
		color[id] = grey
		for _, to := range edges[id] {
			switch color[to] {
			case grey:
				return to
			case white:
				if c := visit(to); c != "" {
					return c
				}
			}
		}
		color[id] = black
		return ""
	}
	for _, s := range steps {
		if color[s.ID] == white {
			if c := visit(s.ID); c != "" {
				return c
			}
		}
	}
	return ""
}

// StepRecord 是运行中一步的记录。
type StepRecord struct {
	StepID string              `json:"step_id"`
	CallID ids.CallID          `json:"call_id"`
	State  consts.RequestState `json:"state"`
	Output map[string]any      `json:"output,omitempty"`
	Next   string              `json:"next,omitempty"` // 所选的下一步，空 = 未选或结束
}

// RunStop 记录运行停止的步骤和原因。
type RunStop struct {
	Reason consts.StopReason `json:"reason"`
	StepID string            `json:"step_id,omitempty"`
	Detail string            `json:"detail,omitempty"`
	At     time.Time         `json:"at"`
}

// Run 是任务的一次运行。运行开始时绑定任务版本和摘要，之后不变（61 第 7.3 节）。
type Run struct {
	RunID         string          `json:"run_id"`
	TaskID        string          `json:"task_id"`
	TaskVersion   int64           `json:"task_version"`
	TaskDigest    ids.Digest      `json:"task_digest"`
	SessionID     string          `json:"session_id"`
	Scope         request.Scope   `json:"scope"`
	Params        map[string]any  `json:"params,omitempty"`
	State         consts.RunState `json:"state"`
	Current       string          `json:"current,omitempty"` // 当前步骤 ID
	Steps         []StepRecord    `json:"steps,omitempty"`
	MissingInputs []string        `json:"missing_inputs,omitempty"` // 缺的必需参数或 K
	Stop          *RunStop        `json:"stop,omitempty"`
	Version       ids.Version     `json:"version"`   // 乐观锁版本号
	EventSeq      int64           `json:"event_seq"` // RunID 事件流上最后一个事件的序号
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// Clone 深拷贝。
func (r *Run) Clone() *Run {
	if r == nil {
		return nil
	}
	c := *r
	c.Scope = r.Scope.Clone()
	c.Params = request.CloneMap(r.Params)
	if r.Steps != nil {
		c.Steps = make([]StepRecord, len(r.Steps))
		for i, s := range r.Steps {
			s.Output = request.CloneMap(s.Output)
			c.Steps[i] = s
		}
	}
	c.MissingInputs = slices.Clone(r.MissingInputs)
	if r.Stop != nil {
		s := *r.Stop
		c.Stop = &s
	}
	return &c
}
