// Package orchestrator 实现既定任务编排引擎（61 第 7.3、7.4 节）。
// 引擎只做三件事：校验并登记前后的任务定义、为每一步生成执行申请、
// 在步骤申请的状态变化后推进任务运行。申请本身由流水线驱动，这里不执行任何东西。
package orchestrator

import (
	"fmt"
	"strings"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/orchestrator/expr"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// ParamsPrefix 是分支条件里任务参数的前缀；它不能同时是步骤 ID。
const ParamsPrefix = "params"

// ConditionSchema 按任务定义生成分支条件的类型表：
// params.<name> 来自参数 schema，<stepID>.<field> 来自每一步声明的输出。
func ConditionSchema(def *task.Definition) expr.Schema {
	s := expr.Schema{}
	for _, p := range def.Params {
		s[ParamsPrefix+"."+p.Name] = exprType(p.Type)
	}
	for _, st := range def.Steps {
		for _, o := range st.Outputs {
			s[st.ID+"."+o.Name] = exprType(o.Type)
		}
	}
	return s
}

// exprType 把任务字段类型映射到表达式类型。类型已由 Definition.Validate 保证合法。
func exprType(t task.FieldType) expr.Type {
	elem := func(k string) expr.Type { return expr.Type{Kind: scalarKind(k)} }
	if t.Kind == task.KindList {
		return expr.ListOf(elem(t.Elem))
	}
	return expr.Type{Kind: scalarKind(t.Kind)}
}

func scalarKind(k string) expr.Kind {
	switch k {
	case task.KindString:
		return expr.KindString
	case task.KindNumber:
		return expr.KindNumber
	case task.KindBool:
		return expr.KindBool
	}
	return expr.KindInvalid
}

// CompileBranches 编译全部分支条件。返回值以步骤 ID 为键，切片与该步 Branches 一一对应。
// 任何一条不通过都返回 core.task_invalid，并包装表达式的错误（错误码留在消息里）。
// 调用前应已通过 Definition.Validate；这里不重复结构校验。
func CompileBranches(def *task.Definition) (map[string][]*expr.Program, error) {
	schema := ConditionSchema(def)
	out := make(map[string][]*expr.Program, len(def.Steps))
	var problems []string
	var first error
	for _, st := range def.Steps {
		if len(st.Branches) == 0 {
			continue
		}
		progs := make([]*expr.Program, len(st.Branches))
		for i, b := range st.Branches {
			p, err := expr.Compile(b.When, schema)
			if err != nil {
				problems = append(problems, fmt.Sprintf("step %q branch #%d: %v", st.ID, i, err))
				if first == nil {
					first = err
				}
				continue
			}
			progs[i] = p
		}
		out[st.ID] = progs
	}
	if len(problems) > 0 {
		return nil, errcode.Wrap(errcode.TaskInvalid, strings.Join(problems, "; "), first)
	}
	return out, nil
}

// CheckDefinition 是登记时的校验（61 第 7.4 节）：结构校验，再对每个分支条件
// 按参数 schema 和各步骤输出 schema 做语法和类型检查。不合格返回 core.task_invalid。
func CheckDefinition(def *task.Definition) error {
	_, err := checkAndCompile(def)
	return err
}

// checkAndCompile 做登记校验并返回编译好的分支条件。
func checkAndCompile(def *task.Definition) (map[string][]*expr.Program, error) {
	if def == nil {
		return nil, errcode.New(errcode.TaskInvalid, "definition is nil")
	}
	if err := def.Validate(); err != nil {
		return nil, err
	}
	for _, st := range def.Steps {
		if st.ID == ParamsPrefix {
			return nil, errcode.Newf(errcode.TaskInvalid, "step id %q is reserved for task parameters", ParamsPrefix)
		}
	}
	return CompileBranches(def)
}
