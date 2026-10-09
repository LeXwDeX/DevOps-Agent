package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// Deps 是流水线需要的全部端口。字段都必须非空。
type Deps struct {
	Requests    ports.RequestStore
	Revisions   ports.RevisionLog
	Policy      ports.PolicySource
	Registry    ports.Registry
	Scope       ports.ScopeSource
	Preview     ports.PreviewPort
	Fingerprint ports.FingerprintPort
	DCG         ports.DCGPort
	Executor    ports.ExecutorPort
	Audit       ports.AuditSink
	Clock       ports.Clock
	IDs         ports.IDGen
}

// Service 是检验流水线。无内部可变状态，可并发使用；并发修改同一申请靠存储端口的 CAS 保护。
type Service struct {
	d Deps
}

// New 创建流水线。任何端口为空返回 core.invalid_argument。
func New(d Deps) (*Service, error) {
	missing := ""
	switch {
	case d.Requests == nil:
		missing = "Requests"
	case d.Revisions == nil:
		missing = "Revisions"
	case d.Policy == nil:
		missing = "Policy"
	case d.Registry == nil:
		missing = "Registry"
	case d.Scope == nil:
		missing = "Scope"
	case d.Preview == nil:
		missing = "Preview"
	case d.Fingerprint == nil:
		missing = "Fingerprint"
	case d.DCG == nil:
		missing = "DCG"
	case d.Executor == nil:
		missing = "Executor"
	case d.Audit == nil:
		missing = "Audit"
	case d.Clock == nil:
		missing = "Clock"
	case d.IDs == nil:
		missing = "IDs"
	}
	if missing != "" {
		return nil, errcode.Newf(errcode.InvalidArgument, "pipeline: port %s is required", missing)
	}
	return &Service{d: d}, nil
}

// txn 是对一个申请的一次读改写：读出克隆、就地修改、边改边收集事件、最后一次 CAS 保存。
type txn struct {
	s        *Service
	r        *request.ExecutionRequest
	expected ids.Version
	evts     []events.Event
}

// begin 读出申请，记下读到的版本作为 CAS 的 expected。
func (s *Service) begin(ctx context.Context, id ids.CallID) (*txn, error) {
	if !id.Valid() {
		return nil, errcode.New(errcode.InvalidArgument, "call id is required")
	}
	r, err := s.d.Requests.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &txn{s: s, r: r, expected: r.Version}, nil
}

// emit 按 r 当前状态构造一条事件。调用时机决定事件的 Status，所以要在对应转换之后调用。
func (t *txn) emit(typ events.Type, data map[string]any) {
	t.evts = append(t.evts, events.ForRequest(t.r, typ, data, t.s.d.Clock.Now(), t.s.d.IDs.NewID("event")))
}

// save 设置 Version 并带着已收集的事件做 CAS 保存。成功后 t 可继续用于下一次读改写。
func (t *txn) save(ctx context.Context) (*request.ExecutionRequest, error) {
	t.r.Version = t.expected.Next()
	if err := t.s.d.Requests.Save(ctx, t.r, t.expected, t.evts); err != nil {
		return nil, err
	}
	t.expected = t.r.Version
	t.evts = nil
	return t.r, nil
}

func (s *Service) now() time.Time { return s.d.Clock.Now() }

// revData 是多数事件共用的数据：修订号和内容摘要。
func revData(r *request.ExecutionRequest) map[string]any {
	return map[string]any{"revision": int(r.Revision), "digest": string(r.Digest)}
}

func hasCode(err error, c errcode.Code) bool {
	got, ok := errcode.CodeOf(err)
	return ok && got == c
}

// reasonOf 取带码错误的消息部分（不含错误码前缀）。
func reasonOf(err error) string {
	var e *errcode.Error
	if errors.As(err, &e) {
		return e.Msg
	}
	return err.Error()
}
