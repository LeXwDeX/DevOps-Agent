package pgstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	schema "github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/core"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// model 与持久化文档之间的转换（62 第 3.3 节）。
//
// 写出（model -> 文档）：
//   - 时间归一为 UTC、微秒精度（和 timestamptz 一致）；
//   - 类 JSON 的 map 经 JSON 往返：整数值读回为 int，其余数字读回为 float64，
//     切片读回为 []any（摘要按十进制文本规范化，不受这些类型变化影响）。
//
// 读入（文档 -> model）：
//   - 严格解码：未知字段、多余内容、未知 schema_id 都返回 storage.decode_failed；
//   - 枚举按 consts.ParseX 校验，非法值返回 storage.decode_failed；不允许为空的枚举为空也是错。

// ---- 严格解码 ----

// decodeDoc 把存储的 JSON 严格解码到 v。
func decodeDoc(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing data after document")
	}
	return nil
}

// decodeStored 检查 schema_id，再严格解码。what 用于错误信息（不含文档内容）。
func decodeStored(what, gotSchema, wantSchema string, raw []byte, v any) error {
	if gotSchema != wantSchema {
		return errcode.Newf(errcode.StorageDecodeFailed, "%s: unknown schema_id %q (want %q)", what, gotSchema, wantSchema)
	}
	if err := decodeDoc(raw, v); err != nil {
		return decodeFailed(what, err)
	}
	return nil
}

// numberize 把解码出的 json.Number 换成 int（整数）或 float64，原地处理类 JSON 值。
func numberize(v any) (any, error) {
	switch x := v.(type) {
	case json.Number:
		s := string(x)
		if !strings.ContainsAny(s, ".eE") {
			if i, err := strconv.ParseInt(s, 10, 64); err == nil {
				return int(i), nil
			}
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("number %q: %w", s, err)
		}
		return f, nil
	case map[string]any:
		for k, e := range x {
			n, err := numberize(e)
			if err != nil {
				return nil, err
			}
			x[k] = n
		}
		return x, nil
	case []any:
		for i, e := range x {
			n, err := numberize(e)
			if err != nil {
				return nil, err
			}
			x[i] = n
		}
		return x, nil
	}
	return v, nil
}

// fixMap 处理文档里的 map 字段；nil 保持 nil。
func fixMap(d *decoder, field string, m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out, err := numberize(m)
	if err != nil {
		d.fail(field, err)
		return m
	}
	return out.(map[string]any)
}

// decoder 记录文档转换中的第一个错误。
type decoder struct{ err error }

func (d *decoder) fail(field string, err error) {
	if d.err == nil {
		d.err = errcode.Wrap(errcode.StorageDecodeFailed, "field "+field, err)
	}
}

// enum 校验并转换枚举字段。required 为 false 时，空串表示"未设置"。
func enum[T ~string](d *decoder, field, s string, required bool, parse func(string) (T, error)) T {
	var zero T
	if s == "" && !required {
		return zero
	}
	v, err := parse(s)
	if err != nil {
		d.fail(field, err)
		return zero
	}
	return v
}

// ---- 公共小工具 ----

// ut 归一时间：UTC、微秒精度。
func ut(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

func utPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := ut(*t)
	return &v
}

func ptr[T any](v T) *T { return &v }

func cloneStrs(in []string) []string {
	if in == nil {
		return nil
	}
	return slices.Clone(in)
}

// mapSlice 转换切片，nil 保持 nil。
func mapSlice[A, B any](in []A, f func(A) B) []B {
	if in == nil {
		return nil
	}
	out := make([]B, len(in))
	for i, e := range in {
		out[i] = f(e)
	}
	return out
}

func scopeToDoc(s request.Scope) schema.ScopeDoc {
	return schema.ScopeDoc{Project: s.Project, Environment: s.Environment, Targets: cloneStrs(s.Targets)}
}

func scopeFromDoc(s schema.ScopeDoc) request.Scope {
	return request.Scope{Project: s.Project, Environment: s.Environment, Targets: cloneStrs(s.Targets)}
}

func unitToDoc(u request.UnitRef) schema.UnitRefDoc {
	return schema.UnitRefDoc{ID: u.ID, Version: u.Version, Digest: string(u.Digest)}
}

func unitFromDoc(u schema.UnitRefDoc) request.UnitRef {
	return request.UnitRef{ID: u.ID, Version: u.Version, Digest: ids.Digest(u.Digest)}
}

func selfReviewToDoc(s *request.SelfReview) *schema.SelfReviewDoc {
	if s == nil {
		return nil
	}
	return &schema.SelfReviewDoc{Verdict: string(s.Verdict), Reason: s.Reason}
}

func selfReviewFromDoc(d *decoder, field string, s *schema.SelfReviewDoc) *request.SelfReview {
	if s == nil {
		return nil
	}
	return &request.SelfReview{
		Verdict: enum(d, field+".verdict", s.Verdict, true, consts.ParseSelfReviewVerdict),
		Reason:  s.Reason,
	}
}

// ---- 执行申请 ----

func requestToDoc(r *request.ExecutionRequest) schema.RequestDocV1 {
	doc := schema.RequestDocV1{
		CallID:    string(r.CallID),
		Revision:  int(r.Revision),
		Digest:    string(r.Digest),
		Source:    string(r.Source),
		SessionID: r.SessionID,
		Scope:     scopeToDoc(r.Scope),
		Content: schema.ContentDoc{
			Rendered:   r.Content.Rendered,
			Params:     r.Content.Params,
			SecretKeys: cloneStrs(r.Content.SecretKeys),
		},
		Expectation: schema.ExpectationDoc{
			Intent:   r.Expectation.Intent,
			Targets:  cloneStrs(r.Expectation.Targets),
			Category: string(r.Expectation.Category),
			ExpectedChange: schema.ExpectedChangeDoc{
				Text: r.Expectation.ExpectedChange.Text,
				Ranges: mapSlice(r.Expectation.ExpectedChange.Ranges, func(v request.ValueRange) schema.ValueRangeDoc {
					return schema.ValueRangeDoc{Field: v.Field, Min: v.Min, Max: v.Max}
				}),
			},
			ExpectedOutput: r.Expectation.ExpectedOutput,
			SuccessCriteria: mapSlice(r.Expectation.SuccessCriteria, func(c request.Criterion) schema.CriterionDoc {
				return schema.CriterionDoc{Expr: c.Expr, Text: c.Text}
			}),
			Reversible: string(r.Expectation.Reversible),
			Rollback:   r.Expectation.Rollback,
			HasPreview: r.Expectation.HasPreview,
			RiskNote:   r.Expectation.RiskNote,
		},
		Submission: schema.SubmissionDoc{
			SelfReview:        selfReviewToDoc(r.Submission.SelfReview),
			HookRequireManual: r.Submission.HookRequireManual,
			HookReasons:       cloneStrs(r.Submission.HookReasons),
			AnalysisFailed:    r.Submission.AnalysisFailed,
			AnalysisReason:    r.Submission.AnalysisReason,
		},
		Checks: schema.ChecksDoc{
			SelfReview:  selfReviewToDoc(r.Checks.SelfReview),
			Escalations: cloneStrs(r.Checks.Escalations),
		},
		Versions: schema.VersionsDoc{
			SchemaID:                 r.Versions.SchemaID,
			PolicyVersion:            r.Versions.PolicyVersion,
			PolicyLayer:              string(r.Versions.PolicyLayer),
			DCGRulesetVersion:        r.Versions.DCGRulesetVersion,
			FingerprintMethodVersion: r.Versions.FingerprintMethodVersion,
		},
		State:     string(r.State),
		Version:   int64(r.Version),
		EventSeq:  r.EventSeq,
		CreatedAt: ut(r.CreatedAt),
		UpdatedAt: ut(r.UpdatedAt),
	}
	if r.Task != nil {
		doc.Task = &schema.TaskRefDoc{RunID: r.Task.RunID, StepID: r.Task.StepID}
	}
	if r.Content.Unit != nil {
		u := unitToDoc(*r.Content.Unit)
		doc.Content.Unit = &u
	}
	if f := r.Fingerprint; f != nil {
		doc.Fingerprint = &schema.FingerprintDoc{
			Method: f.Method, MethodVersion: f.MethodVersion, CollectedAt: ut(f.CollectedAt),
			Status: string(f.Status), Value: f.Value,
		}
	}
	if p := r.Checks.Preview; p != nil {
		doc.Checks.Preview = &schema.PreviewDoc{Status: string(p.Status), Output: p.Output, Ref: p.Ref, At: ut(p.At)}
	}
	if c := r.Checks.DCG; c != nil {
		doc.Checks.DCG = &schema.DCGCheckDoc{
			Result: string(c.Result), RuleIDs: cloneStrs(c.RuleIDs), RulesetVersion: c.RulesetVersion, At: ut(c.At),
		}
	}
	if p := r.Checks.Policy; p != nil {
		doc.Checks.Policy = &schema.PolicyDecisionDoc{
			Mode: string(p.Mode), Layer: string(p.Layer), AllowAuto: p.AllowAuto,
			PolicyVersion: p.PolicyVersion, Reasons: cloneStrs(p.Reasons),
		}
	}
	doc.Reviews = mapSlice(r.Reviews, func(e request.ReviewEntry) schema.ReviewEntryDoc {
		return schema.ReviewEntryDoc{
			Action: string(e.Action), Comment: e.Comment, Actor: e.Actor, At: ut(e.At),
			Revision: int(e.Revision), Digest: string(e.Digest),
		}
	})
	if x := r.Rejection; x != nil {
		doc.Rejection = &schema.RejectionDoc{Source: string(x.Source), Code: string(x.Code), Reason: x.Reason}
	}
	if x := r.Stop; x != nil {
		doc.Stop = &schema.StopDoc{Reason: string(x.Reason), Detail: x.Detail, At: ut(x.At)}
	}
	if x := r.Frozen; x != nil {
		doc.Frozen = &schema.FrozenDoc{Digest: string(x.Digest), At: ut(x.At)}
	}
	if x := r.Execution; x != nil {
		e := &schema.ExecutionDoc{
			StartedAt: ut(x.StartedAt), EndedAt: ut(x.EndedAt), Outcome: string(x.Outcome), Detail: x.Detail,
		}
		if res := x.Result; res != nil {
			e.Result = &schema.ExecutionResultDoc{
				ExitCode: cloneInt(res.ExitCode), OutputRef: res.OutputRef, Output: res.Output, Err: res.Err,
				StartedAt: ut(res.StartedAt), EndedAt: ut(res.EndedAt),
			}
		}
		doc.Execution = e
	}
	return doc
}

func cloneInt(p *int) *int {
	if p == nil {
		return nil
	}
	return ptr(*p)
}

func clonePF(p *float64) *float64 {
	if p == nil {
		return nil
	}
	return ptr(*p)
}

func requestFromDoc(doc schema.RequestDocV1) (*request.ExecutionRequest, error) {
	d := &decoder{}
	r := &request.ExecutionRequest{
		CallID:    ids.CallID(doc.CallID),
		Revision:  ids.Revision(doc.Revision),
		Digest:    ids.Digest(doc.Digest),
		Source:    enum(d, "source", doc.Source, true, consts.ParseRequestSource),
		SessionID: doc.SessionID,
		Scope:     scopeFromDoc(doc.Scope),
		Content: request.Content{
			Rendered:   doc.Content.Rendered,
			Params:     fixMap(d, "content.params", doc.Content.Params),
			SecretKeys: cloneStrs(doc.Content.SecretKeys),
		},
		Expectation: request.Expectation{
			Intent:   doc.Expectation.Intent,
			Targets:  cloneStrs(doc.Expectation.Targets),
			Category: enum(d, "expectation.category", doc.Expectation.Category, false, consts.ParseCategory),
			ExpectedChange: request.ExpectedChange{
				Text: doc.Expectation.ExpectedChange.Text,
				Ranges: mapSlice(doc.Expectation.ExpectedChange.Ranges, func(v schema.ValueRangeDoc) request.ValueRange {
					return request.ValueRange{Field: v.Field, Min: clonePF(v.Min), Max: clonePF(v.Max)}
				}),
			},
			ExpectedOutput: doc.Expectation.ExpectedOutput,
			SuccessCriteria: mapSlice(doc.Expectation.SuccessCriteria, func(c schema.CriterionDoc) request.Criterion {
				return request.Criterion{Expr: c.Expr, Text: c.Text}
			}),
			Reversible: enum(d, "expectation.reversible", doc.Expectation.Reversible, false, consts.ParseReversibility),
			Rollback:   doc.Expectation.Rollback,
			HasPreview: doc.Expectation.HasPreview,
			RiskNote:   doc.Expectation.RiskNote,
		},
		Submission: request.Submission{
			SelfReview:        selfReviewFromDoc(d, "submission.self_review", doc.Submission.SelfReview),
			HookRequireManual: doc.Submission.HookRequireManual,
			HookReasons:       cloneStrs(doc.Submission.HookReasons),
			AnalysisFailed:    doc.Submission.AnalysisFailed,
			AnalysisReason:    doc.Submission.AnalysisReason,
		},
		Checks: request.Checks{
			SelfReview:  selfReviewFromDoc(d, "checks.self_review", doc.Checks.SelfReview),
			Escalations: cloneStrs(doc.Checks.Escalations),
		},
		Versions: request.Versions{
			SchemaID:                 doc.Versions.SchemaID,
			PolicyVersion:            doc.Versions.PolicyVersion,
			PolicyLayer:              enum(d, "versions.policy_layer", doc.Versions.PolicyLayer, false, consts.ParsePolicyLayer),
			DCGRulesetVersion:        doc.Versions.DCGRulesetVersion,
			FingerprintMethodVersion: doc.Versions.FingerprintMethodVersion,
		},
		State:     enum(d, "state", doc.State, true, consts.ParseRequestState),
		Version:   ids.Version(doc.Version),
		EventSeq:  doc.EventSeq,
		CreatedAt: ut(doc.CreatedAt),
		UpdatedAt: ut(doc.UpdatedAt),
	}
	if t := doc.Task; t != nil {
		r.Task = &request.TaskRef{RunID: t.RunID, StepID: t.StepID}
	}
	if u := doc.Content.Unit; u != nil {
		v := unitFromDoc(*u)
		r.Content.Unit = &v
	}
	if f := doc.Fingerprint; f != nil {
		r.Fingerprint = &request.Fingerprint{
			Method: f.Method, MethodVersion: f.MethodVersion, CollectedAt: ut(f.CollectedAt),
			Status: enum(d, "fingerprint.status", f.Status, true, consts.ParseFingerprintStatus), Value: f.Value,
		}
	}
	if p := doc.Checks.Preview; p != nil {
		r.Checks.Preview = &request.PreviewResult{
			Status: enum(d, "checks.preview.status", p.Status, true, consts.ParsePreviewStatus),
			Output: p.Output, Ref: p.Ref, At: ut(p.At),
		}
	}
	if c := doc.Checks.DCG; c != nil {
		r.Checks.DCG = &request.DCGCheck{
			Result:  enum(d, "checks.dcg.result", c.Result, true, consts.ParseDCGResult),
			RuleIDs: cloneStrs(c.RuleIDs), RulesetVersion: c.RulesetVersion, At: ut(c.At),
		}
	}
	if p := doc.Checks.Policy; p != nil {
		r.Checks.Policy = &policy.Decision{
			Mode:          enum(d, "checks.policy.mode", p.Mode, true, consts.ParsePolicyMode),
			Layer:         enum(d, "checks.policy.layer", p.Layer, true, consts.ParsePolicyLayer),
			AllowAuto:     p.AllowAuto,
			PolicyVersion: p.PolicyVersion,
			Reasons:       cloneStrs(p.Reasons),
		}
	}
	r.Reviews = mapSlice(doc.Reviews, func(e schema.ReviewEntryDoc) request.ReviewEntry {
		return request.ReviewEntry{
			Action:  enum(d, "reviews.action", e.Action, true, consts.ParseHumanAction),
			Comment: e.Comment, Actor: e.Actor, At: ut(e.At),
			Revision: ids.Revision(e.Revision), Digest: ids.Digest(e.Digest),
		}
	})
	if x := doc.Rejection; x != nil {
		r.Rejection = &request.Rejection{
			Source: enum(d, "rejection.source", x.Source, true, consts.ParseRejectionSource),
			Code:   errcode.Code(x.Code), Reason: x.Reason,
		}
	}
	if x := doc.Stop; x != nil {
		r.Stop = &request.Stop{
			Reason: enum(d, "stop.reason", x.Reason, true, consts.ParseStopReason), Detail: x.Detail, At: ut(x.At),
		}
	}
	if x := doc.Frozen; x != nil {
		r.Frozen = &request.Frozen{Digest: ids.Digest(x.Digest), At: ut(x.At)}
	}
	if x := doc.Execution; x != nil {
		e := &request.Execution{
			StartedAt: ut(x.StartedAt), EndedAt: ut(x.EndedAt),
			Outcome: enum(d, "execution.outcome", x.Outcome, false, consts.ParseRequestState), Detail: x.Detail,
		}
		if res := x.Result; res != nil {
			e.Result = &request.ExecutionResult{
				ExitCode: cloneInt(res.ExitCode), OutputRef: res.OutputRef,
				Output: fixMap(d, "execution.result.output", res.Output), Err: res.Err,
				StartedAt: ut(res.StartedAt), EndedAt: ut(res.EndedAt),
			}
		}
		r.Execution = e
	}
	if d.err != nil {
		return nil, d.err
	}
	return r, nil
}

// ---- 任务运行 ----

func runToDoc(r *task.Run) schema.RunDocV1 {
	doc := schema.RunDocV1{
		RunID: r.RunID, TaskID: r.TaskID, TaskVersion: r.TaskVersion, TaskDigest: string(r.TaskDigest),
		SessionID: r.SessionID, Scope: scopeToDoc(r.Scope), Params: r.Params,
		State: string(r.State), Current: r.Current,
		Steps: mapSlice(r.Steps, func(s task.StepRecord) schema.StepRecordDoc {
			return schema.StepRecordDoc{
				StepID: s.StepID, CallID: string(s.CallID), State: string(s.State), Output: s.Output, Next: s.Next,
			}
		}),
		MissingInputs: cloneStrs(r.MissingInputs),
		Version:       int64(r.Version), EventSeq: r.EventSeq,
		CreatedAt: ut(r.CreatedAt), UpdatedAt: ut(r.UpdatedAt),
	}
	if s := r.Stop; s != nil {
		doc.Stop = &schema.RunStopDoc{Reason: string(s.Reason), StepID: s.StepID, Detail: s.Detail, At: ut(s.At)}
	}
	return doc
}

func runFromDoc(doc schema.RunDocV1) (*task.Run, error) {
	d := &decoder{}
	r := &task.Run{
		RunID: doc.RunID, TaskID: doc.TaskID, TaskVersion: doc.TaskVersion, TaskDigest: ids.Digest(doc.TaskDigest),
		SessionID: doc.SessionID, Scope: scopeFromDoc(doc.Scope), Params: fixMap(d, "params", doc.Params),
		State: enum(d, "state", doc.State, true, consts.ParseRunState), Current: doc.Current,
		Steps: mapSlice(doc.Steps, func(s schema.StepRecordDoc) task.StepRecord {
			return task.StepRecord{
				StepID: s.StepID, CallID: ids.CallID(s.CallID),
				State:  enum(d, "steps.state", s.State, false, consts.ParseRequestState),
				Output: fixMap(d, "steps.output", s.Output), Next: s.Next,
			}
		}),
		MissingInputs: cloneStrs(doc.MissingInputs),
		Version:       ids.Version(doc.Version), EventSeq: doc.EventSeq,
		CreatedAt: ut(doc.CreatedAt), UpdatedAt: ut(doc.UpdatedAt),
	}
	if s := doc.Stop; s != nil {
		r.Stop = &task.RunStop{
			Reason: enum(d, "stop.reason", s.Reason, true, consts.ParseStopReason),
			StepID: s.StepID, Detail: s.Detail, At: ut(s.At),
		}
	}
	if d.err != nil {
		return nil, d.err
	}
	return r, nil
}

// ---- 任务定义 ----

func fieldsToDoc(in []task.FieldSpec) []schema.FieldSpecDoc {
	return mapSlice(in, func(f task.FieldSpec) schema.FieldSpecDoc {
		return schema.FieldSpecDoc{Name: f.Name, Type: schema.FieldTypeDoc{Kind: f.Type.Kind, Elem: f.Type.Elem}, Required: f.Required}
	})
}

func fieldsFromDoc(in []schema.FieldSpecDoc) []task.FieldSpec {
	return mapSlice(in, func(f schema.FieldSpecDoc) task.FieldSpec {
		return task.FieldSpec{Name: f.Name, Type: task.FieldType{Kind: f.Type.Kind, Elem: f.Type.Elem}, Required: f.Required}
	})
}

func definitionToDoc(def *task.Definition) schema.TaskDefinitionDocV1 {
	return schema.TaskDefinitionDocV1{
		ID: def.ID, Version: def.Version, Digest: string(def.Digest),
		Params: fieldsToDoc(def.Params), SecretKeys: cloneStrs(def.SecretKeys), Entry: def.Entry,
		Steps: mapSlice(def.Steps, func(s task.Step) schema.TaskStepDoc {
			return schema.TaskStepDoc{
				ID: s.ID, Unit: unitToDoc(s.Unit), Outputs: fieldsToDoc(s.Outputs), Next: s.Next,
				Branches: mapSlice(s.Branches, func(b task.Branch) schema.BranchDoc {
					return schema.BranchDoc{When: b.When, To: b.To}
				}),
			}
		}),
	}
}

func definitionFromDoc(doc schema.TaskDefinitionDocV1) *task.Definition {
	return &task.Definition{
		ID: doc.ID, Version: doc.Version, Digest: ids.Digest(doc.Digest),
		Params: fieldsFromDoc(doc.Params), SecretKeys: cloneStrs(doc.SecretKeys), Entry: doc.Entry,
		Steps: mapSlice(doc.Steps, func(s schema.TaskStepDoc) task.Step {
			return task.Step{
				ID: s.ID, Unit: unitFromDoc(s.Unit), Outputs: fieldsFromDoc(s.Outputs), Next: s.Next,
				Branches: mapSlice(s.Branches, func(b schema.BranchDoc) task.Branch {
					return task.Branch{When: b.When, To: b.To}
				}),
			}
		}),
	}
}

// ---- 事件 ----

func eventToDoc(e events.Event) schema.EventDocV1 {
	return schema.EventDocV1{
		SchemaVersion: e.SchemaVersion, EventID: e.EventID, Type: string(e.Type), StreamID: e.StreamID,
		Sequence: e.Sequence, OccurredAt: ut(e.OccurredAt), SourceKind: e.SourceKind,
		SessionID: e.SessionID, RunID: e.RunID, CallID: string(e.CallID), Revision: int(e.Revision),
		Project: e.Project, Environment: e.Environment, Targets: cloneStrs(e.Targets), Status: e.Status, Data: e.Data,
	}
}

func eventFromDoc(doc schema.EventDocV1) (events.Event, error) {
	d := &decoder{}
	t := events.Type(doc.Type)
	if !t.Valid() {
		d.fail("type", fmt.Errorf("unknown event type %q", doc.Type))
	}
	e := events.Event{
		SchemaVersion: doc.SchemaVersion, EventID: doc.EventID, Type: t, StreamID: doc.StreamID,
		Sequence: doc.Sequence, OccurredAt: ut(doc.OccurredAt), SourceKind: doc.SourceKind,
		SessionID: doc.SessionID, RunID: doc.RunID, CallID: ids.CallID(doc.CallID), Revision: ids.Revision(doc.Revision),
		Project: doc.Project, Environment: doc.Environment, Targets: cloneStrs(doc.Targets), Status: doc.Status,
		Data: fixMap(d, "data", doc.Data),
	}
	if d.err != nil {
		return events.Event{}, d.err
	}
	return e, nil
}

// ---- 审计 ----

func auditToDoc(rec ports.AuditRecord) schema.AuditRecordDocV1 {
	return schema.AuditRecordDocV1{
		At: ut(rec.At), Actor: rec.Actor, Action: rec.Action, CallID: string(rec.CallID),
		Revision: int(rec.Revision), RunID: rec.RunID, Detail: rec.Detail,
	}
}

func auditFromDoc(doc schema.AuditRecordDocV1) (ports.AuditRecord, error) {
	d := &decoder{}
	rec := ports.AuditRecord{
		At: ut(doc.At), Actor: doc.Actor, Action: doc.Action, CallID: ids.CallID(doc.CallID),
		Revision: ids.Revision(doc.Revision), RunID: doc.RunID, Detail: fixMap(d, "detail", doc.Detail),
	}
	if d.err != nil {
		return ports.AuditRecord{}, d.err
	}
	return rec, nil
}
