// Package ports 定义 core 需要的全部端口接口（61 第 8.3 节）。
// core 只依赖这些接口；真实实现在 provider，内存替身在 core/coretest。
// 所有方法第一个参数是 context.Context。
package ports

import (
	"context"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
)

// RequestStore 读写执行申请。实现必须存取克隆，不与调用方共享内存。
// 保存申请和待发事件在同一事务里完成（outbox，61 第 8.2 节）。
type RequestStore interface {
	// Get 返回申请；不存在返回 core.not_found。
	Get(ctx context.Context, id ids.CallID) (*request.ExecutionRequest, error)
	// Create 保存新申请（草稿）和它的事件；CallID 已存在返回 core.already_exists。
	Create(ctx context.Context, r *request.ExecutionRequest, evts []events.Event) error
	// Save 做 CAS：已存版本不等于 expected 时返回 core.cas_conflict，什么都不写。
	// 调用方已把 r.Version 设为 expected.Next()。申请不存在返回 core.not_found。
	Save(ctx context.Context, r *request.ExecutionRequest, expected ids.Version, evts []events.Event) error
	// ListNonTerminal 返回全部非终态申请，供恢复使用。
	ListNonTerminal(ctx context.Context) ([]*request.ExecutionRequest, error)
}

// RevisionLog 是修订日志，只追加不覆盖（K-14、K-15）。
type RevisionLog interface {
	// Append 追加一个被取代的旧修订（完整副本）。
	Append(ctx context.Context, old *request.ExecutionRequest) error
	// List 按追加顺序返回该调用的全部旧修订。
	List(ctx context.Context, id ids.CallID) ([]*request.ExecutionRequest, error)
}

// TaskStore 保存任务定义（含启用指针）和任务运行。
type TaskStore interface {
	// Definition 返回指定版本的任务定义；不存在返回 core.not_found。
	Definition(ctx context.Context, id string, version int64) (*task.Definition, error)
	// ActiveVersion 返回当前启用的版本；任务不存在或没有启用版本返回 core.not_found。
	ActiveVersion(ctx context.Context, id string) (int64, error)
	GetRun(ctx context.Context, runID string) (*task.Run, error)
	// CreateRun 保存新运行和事件；RunID 已存在返回 core.already_exists。
	CreateRun(ctx context.Context, run *task.Run, evts []events.Event) error
	// SaveRun 做 CAS，语义同 RequestStore.Save。
	SaveRun(ctx context.Context, run *task.Run, expected ids.Version, evts []events.Event) error
	ListNonTerminalRuns(ctx context.Context) ([]*task.Run, error)
}

// PolicySource 读取已登记的策略层。
type PolicySource interface {
	// Load 返回当前策略集。每次返回独立的副本（含规则里的指针），调用方随意改动不影响端口和后面的调用。
	Load(ctx context.Context) (policy.Set, error)
}

// UnitInfo 是已登记单元的登记信息。Category 和 Reversible 以登记值为准。
type UnitInfo struct {
	Ref                      request.UnitRef
	Subject                  string // provider 或工具键
	Action                   string // 功能名
	Zone                     consts.Zone
	Category                 consts.Category
	Reversible               consts.Reversibility
	HasPreview               bool
	FingerprintMethod        string
	FingerprintMethodVersion string
}

// Registry 查询已登记单元，并为任务步骤生成内容和执行预期（core 不实现模板语法）。
type Registry interface {
	// Unit 返回指定版本的登记信息；不存在返回 core.not_found。
	Unit(ctx context.Context, id string, version int64) (UnitInfo, error)
	// RenderStep 按任务参数和前序步骤输出（键为步骤 ID）生成步骤的内容和执行预期。
	// 不修改入参；返回值不与入参、端口内部状态或后面的调用共享内存。
	RenderStep(ctx context.Context, def *task.Definition, step task.Step, params map[string]any, outputs map[string]map[string]any) (request.Content, request.Expectation, error)
}

// SessionInfo 是会话的项目、环境和模式。
type SessionInfo struct {
	Project     string
	Environment string
	Mode        consts.SessionMode
}

// ScopeSource 查询会话范围。
type ScopeSource interface {
	// Session 返回会话信息；不存在返回 core.not_found。
	Session(ctx context.Context, sessionID string) (SessionInfo, error)
	// OutsideScope 返回不属于该项目和环境的目标（稳定 ID），保持 targets 的顺序；都在范围内返回空。
	// 项目或环境没有登记过，所有目标都算范围外。不修改入参，返回值不与入参共享内存。
	OutsideScope(ctx context.Context, project, env string, targets []string) ([]string, error)
}

// SecretKeys 查询凭证库里哪些 K 已登记（61 第 7.4 节、第 8.3 节）。
// 只回答"有没有"。没有读取 V 的方法，core 永远拿不到 V（K-16）。
type SecretKeys interface {
	// Missing 返回 keys 中在该项目和环境里还没有登记的 K，保持 keys 的顺序、不含重复；都已登记返回空。
	// 只返回 K 名，不返回任何 V。查询出错时返回错误，调用方不得把出错当作"都已登记"。
	Missing(ctx context.Context, project, env string, keys []string) ([]string, error)
}

// PreviewPort 运行原生预演。
type PreviewPort interface {
	// Preview 不修改传入的申请。ctx 已取消时返回错误。
	Preview(ctx context.Context, r *request.ExecutionRequest) (request.PreviewResult, error)
}

// FingerprintPort 采集目标指纹。采集不到时，实现可返回 Status 为 unavailable 的结果，也可返回错误。
type FingerprintPort interface {
	// Collect 不修改传入的申请。Status 为 collected 时 Value 非空；目标没有变化时，连续采集得到相同的 Value（K-04）。
	// ctx 已取消时返回错误。
	Collect(ctx context.Context, r *request.ExecutionRequest) (request.Fingerprint, error)
}

// DCGPort 检查最终内容。返回错误时，调用方按"不可用"处理。
type DCGPort interface {
	// Check 不修改传入的申请；返回值（含 RuleIDs）不与端口内部状态或后面的调用共享内存。ctx 已取消时返回错误。
	Check(ctx context.Context, r *request.ExecutionRequest) (request.DCGCheck, error)
}

// ExecutorPort 提交冻结后的申请并取回结果。
type ExecutorPort interface {
	// Execute 不修改传入的申请；返回值（含 ExitCode 和 Output）不与端口内部状态或后面的调用共享内存。
	// ctx 已取消时返回错误，不执行。
	Execute(ctx context.Context, r *request.ExecutionRequest) (request.ExecutionResult, error)
}

// EventSink 发出不伴随状态保存的事件，例如恢复开始和结束。
type EventSink interface {
	Emit(ctx context.Context, evts ...events.Event) error
}

// AuditRecord 是审计记录。内容由装配层决定，core 只传引用和摘要。
type AuditRecord struct {
	At       time.Time
	Actor    string
	Action   string
	CallID   ids.CallID
	Revision ids.Revision
	RunID    string
	Detail   map[string]any
}

// AuditSink 追加审计记录。
type AuditSink interface {
	// Append 追加一条记录，保持追加顺序；不与调用方共享 Detail 的内存。ctx 已取消时返回错误，什么都不记。
	Append(ctx context.Context, rec AuditRecord) error
}

// Clock 提供时间。
type Clock interface {
	// Now 返回非零时间。同一个实现上，后一次调用不早于前一次；可并发调用。
	Now() time.Time
}

// IDGen 生成 ID。kind 说明用途，例如 "call"、"event"、"run"。
type IDGen interface {
	NewID(kind string) string
}
