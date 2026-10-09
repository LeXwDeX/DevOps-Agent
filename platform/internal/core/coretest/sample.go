package coretest

import (
	"fmt"
	"strings"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/core"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// SampleRequest 返回一个字段齐全的 draft 申请（Version 1），用来做存储测试。
// 它不经过 core/request（coretest 只依赖 ports），内容摘要是真实的。
func SampleRequest(id ids.CallID) *request.ExecutionRequest {
	min := 1.0
	r := &request.ExecutionRequest{
		CallID:    id,
		Revision:  ids.FirstRevision,
		Source:    consts.SourceRegisteredTool,
		SessionID: "session-1",
		Scope:     request.Scope{Project: "proj", Environment: "prod", Targets: []string{"host-a", "host-b"}},
		Content: request.Content{
			Unit:       &request.UnitRef{ID: "unit-1", Version: 1, Digest: ids.Digest(strings.Repeat("a", 64))},
			Rendered:   "systemctl restart nginx",
			Params:     map[string]any{"svc": "nginx", "opts": []any{"a", map[string]any{"k": "v"}}},
			SecretKeys: []string{"TOKEN"},
		},
		Expectation: request.Expectation{
			Intent:          "restart nginx",
			Targets:         []string{"host-a", "host-b"},
			Category:        consts.CategoryStartStop,
			ExpectedChange:  request.ExpectedChange{Text: "nginx restarted", Ranges: []request.ValueRange{{Field: "n", Min: &min}}},
			ExpectedOutput:  "active",
			SuccessCriteria: []request.Criterion{{Expr: "exit_code == 0"}},
			Reversible:      consts.Reversible,
			Rollback:        "restart again",
		},
		Submission: request.Submission{SelfReview: &request.SelfReview{Verdict: consts.VerdictNoRisk}, HookReasons: []string{"h"}},
		Versions:   request.Versions{SchemaID: core.ExecutionRequestV1},
		State:      consts.RequestDraft,
		Version:    1,
		CreatedAt:  Epoch,
		UpdatedAt:  Epoch,
	}
	r.Digest, _ = r.ContentDigest()
	return r
}

// SampleRun 返回一个字段齐全的 pending 运行（Version 1）。
func SampleRun(id string) *task.Run {
	return &task.Run{
		RunID: id, TaskID: "deploy", TaskVersion: 2, TaskDigest: ids.Digest(strings.Repeat("b", 64)),
		SessionID: "session-1",
		Scope:     request.Scope{Project: "proj", Environment: "prod", Targets: []string{"host-a"}},
		Params:    map[string]any{"svc": "nginx", "hosts": []any{"a"}},
		State:     consts.RunPending,
		Steps:     []task.StepRecord{{StepID: "A", Output: map[string]any{"n": []any{1}}}},
		Version:   1,
		CreatedAt: Epoch,
		UpdatedAt: Epoch,
	}
}

// SampleDefinition 返回一个结构合法的两步任务定义。
func SampleDefinition(id string, version int64) *task.Definition {
	return &task.Definition{
		ID: id, Version: version, Digest: ids.Digest(strings.Repeat("c", 64)),
		Params:     []task.FieldSpec{{Name: "svc", Type: task.FieldType{Kind: task.KindString}, Required: true}},
		SecretKeys: []string{"TOKEN"},
		Entry:      "A",
		Steps: []task.Step{
			{ID: "A", Unit: request.UnitRef{ID: "ua", Version: 1}, Branches: []task.Branch{{When: "A.exit_code == 0", To: "B"}}},
			{ID: "B", Unit: request.UnitRef{ID: "ub", Version: 1}},
		},
	}
}

// SampleEvent 返回一个合法的事件信封。
func SampleEvent(stream string, seq int64, t events.Type) events.Event {
	return events.Event{
		SchemaVersion: core.EventV1,
		EventID:       fmt.Sprintf("%s#%d", stream, seq),
		Type:          t,
		StreamID:      stream,
		Sequence:      seq,
		OccurredAt:    Epoch,
		SourceKind:    events.SourceHostConfirmed,
		Targets:       []string{"host-a"},
		Data:          map[string]any{"digest": "d", "list": []any{"x"}},
	}
}

func codeOf(err error) errcode.Code {
	c, _ := errcode.CodeOf(err)
	return c
}
