package pgstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	schema "github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/core"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// HarnessStore 在 harness 库上实现 RequestStore、RevisionLog 和 EventSink，
// 并提供 outbox 的读取与标记（62 第 3.5 节）。并发安全（连接池）。
type HarnessStore struct {
	pool *pgxpool.Pool
}

var (
	_ ports.RequestStore = (*HarnessStore)(nil)
	_ ports.RevisionLog  = (*HarnessStore)(nil)
	_ ports.EventSink    = (*HarnessStore)(nil)
)

// NewHarnessStore 返回使用 harness 库连接池的存储。连接池由调用方创建和关闭。
func NewHarnessStore(pool *pgxpool.Pool) *HarnessStore { return &HarnessStore{pool: pool} }

// encodeDoc 把文档编码为 JSON，并确认它能按读取规则解回来（枚举合法等）。
// 存不回来的数据在写入时就拒绝：core.invalid_argument，而不是等读取时才发现。
func encodeDoc[D, M any](what string, doc D, check func(D) (M, error)) ([]byte, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, what+" cannot be encoded", err)
	}
	var back D
	if err := decodeDoc(raw, &back); err != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, what+" cannot be stored", err)
	}
	if _, err := check(back); err != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, what+" cannot be stored", err)
	}
	return raw, nil
}

func decodeRequest(schemaID string, raw []byte) (*request.ExecutionRequest, error) {
	var doc schema.RequestDocV1
	if err := decodeStored("request", schemaID, schema.ExecutionRequestV1, raw, &doc); err != nil {
		return nil, err
	}
	return requestFromDoc(doc)
}

// ---- RequestStore ----

// Get 按 CallID 读取申请；不存在返回 core.not_found。
func (s *HarnessStore) Get(ctx context.Context, id ids.CallID) (*request.ExecutionRequest, error) {
	var sid string
	var raw []byte
	err := s.pool.QueryRow(ctx, sqlRequestGet, string(id)).Scan(&sid, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errcode.Newf(errcode.NotFound, "call %s", id)
	}
	if err != nil {
		return nil, mapErr(ctx, "get request", err)
	}
	return decodeRequest(sid, raw)
}

// Create 在一个事务里插入申请和事件；CallID 已存在返回 core.already_exists。
func (s *HarnessStore) Create(ctx context.Context, r *request.ExecutionRequest, evts []events.Event) error {
	if r == nil || !r.CallID.Valid() {
		return errcode.New(errcode.InvalidArgument, "request with call id is required")
	}
	raw, err := encodeDoc("request", requestToDoc(r), requestFromDoc)
	if err != nil {
		return err
	}
	rows, err := prepareEvents(evts)
	if err != nil {
		return err
	}
	return withTx(ctx, s.pool, "create request", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sqlRequestInsert,
			string(r.CallID), int64(r.Version), int32(r.Revision), string(r.State), r.SessionID, r.EventSeq,
			ut(r.CreatedAt), ut(r.UpdatedAt), schema.ExecutionRequestV1, raw)
		if isConstraint(err, pgUniqueViolation, conRequestsPkey) {
			return errcode.Newf(errcode.AlreadyExists, "call %s", r.CallID)
		}
		if err != nil {
			return mapErr(ctx, "insert request", err)
		}
		return insertEvents(ctx, tx, rows)
	})
}

// Save 在一个事务里做 CAS 更新并插入事件。已存版本不等于 expected 返回 core.cas_conflict，
// 申请不存在返回 core.not_found；任何一步失败整体回滚。
func (s *HarnessStore) Save(ctx context.Context, r *request.ExecutionRequest, expected ids.Version, evts []events.Event) error {
	if r == nil || !r.CallID.Valid() {
		return errcode.New(errcode.InvalidArgument, "request with call id is required")
	}
	if r.Version != expected.Next() {
		return errcode.Newf(errcode.InvalidArgument, "call %s: version must be expected+1 (%d), got %d", r.CallID, expected.Next(), r.Version)
	}
	raw, err := encodeDoc("request", requestToDoc(r), requestFromDoc)
	if err != nil {
		return err
	}
	rows, err := prepareEvents(evts)
	if err != nil {
		return err
	}
	return withTx(ctx, s.pool, "save request", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sqlRequestUpdate,
			string(r.CallID), int64(expected),
			int64(r.Version), int32(r.Revision), string(r.State), r.SessionID, r.EventSeq,
			ut(r.CreatedAt), ut(r.UpdatedAt), schema.ExecutionRequestV1, raw)
		if err != nil {
			return mapErr(ctx, "update request", err)
		}
		if tag.RowsAffected() == 0 {
			return versionMismatch(ctx, tx, sqlRequestVersion, "call", string(r.CallID), expected)
		}
		return insertEvents(ctx, tx, rows)
	})
}

// versionMismatch 用 sql（按主键查 version）区分 not_found 和 cas_conflict。
func versionMismatch(ctx context.Context, tx pgx.Tx, sql, what, id string, expected ids.Version) error {
	var cur int64
	err := tx.QueryRow(ctx, sql, id).Scan(&cur)
	if errors.Is(err, pgx.ErrNoRows) {
		return errcode.Newf(errcode.NotFound, "%s %s", what, id)
	}
	if err != nil {
		return mapErr(ctx, "check version", err)
	}
	return errcode.Newf(errcode.CASConflict, "%s %s: stored version %d, expected %d", what, id, cur, expected)
}

// ListNonTerminal 返回全部非终态申请，按 CallID 排序。
func (s *HarnessStore) ListNonTerminal(ctx context.Context) ([]*request.ExecutionRequest, error) {
	return queryDocs(ctx, s.pool, "list requests", decodeRequest, sqlRequestListNonTerminal)
}

// ---- RevisionLog ----

// Append 追加一个被取代的旧修订。同一 (CallID, Revision, Digest) 已在日志里时不报错，也不产生第二行。
func (s *HarnessStore) Append(ctx context.Context, old *request.ExecutionRequest) error {
	if old == nil || !old.CallID.Valid() {
		return errcode.New(errcode.InvalidArgument, "request with call id is required")
	}
	raw, err := encodeDoc("revision", requestToDoc(old), requestFromDoc)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, sqlRevisionInsert,
		string(old.CallID), int32(old.Revision), string(old.Digest), schema.ExecutionRequestV1, raw); err != nil {
		return mapErr(ctx, "append revision", err)
	}
	return nil
}

// List 按追加顺序返回该调用的全部旧修订；没有记录返回空列表。
func (s *HarnessStore) List(ctx context.Context, id ids.CallID) ([]*request.ExecutionRequest, error) {
	out, err := queryDocs(ctx, s.pool, "list revisions", decodeRequest, sqlRevisionList, string(id))
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []*request.ExecutionRequest{}
	}
	return out, nil
}

// ---- EventSink ----

// Emit 在一个事务里插入全部事件（不伴随状态保存的事件，例如恢复开始和结束）。
func (s *HarnessStore) Emit(ctx context.Context, evts ...events.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(evts) == 0 {
		return nil
	}
	rows, err := prepareEvents(evts)
	if err != nil {
		return err
	}
	return withTx(ctx, s.pool, "emit events", func(tx pgx.Tx) error { return insertEvents(ctx, tx, rows) })
}

// ---- outbox 读取（供以后的投递服务使用）----

// PendingEvents 按 id 顺序返回未投递的事件，最多 limit 条。
// 至少一次投递：消费者按 EventID 去重。
func (s *HarnessStore) PendingEvents(ctx context.Context, limit int) ([]events.Event, error) {
	if limit <= 0 {
		return nil, errcode.Newf(errcode.InvalidArgument, "limit must be positive, got %d", limit)
	}
	return queryDocs(ctx, s.pool, "pending events", decodeEvent, sqlOutboxPending, int64(limit))
}

// MarkDelivered 把指定事件标记为已投递；不存在或已标记的 ID 忽略。
func (s *HarnessStore) MarkDelivered(ctx context.Context, eventIDs []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(eventIDs) == 0 {
		return nil
	}
	if _, err := s.pool.Exec(ctx, sqlOutboxMarkDelivered, eventIDs); err != nil {
		return mapErr(ctx, "mark delivered", err)
	}
	return nil
}

// queryable 是连接池和事务共有的查询方法。
type queryable interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// queryDocs 执行 SELECT schema_id, doc ...，逐行解码；没有行返回 nil 切片。
func queryDocs[T any](ctx context.Context, q queryable, op string, decode func(schemaID string, raw []byte) (T, error), sql string, args ...any) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(ctx, op, err)
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var sid string
		var raw []byte
		if err := rows.Scan(&sid, &raw); err != nil {
			return nil, mapErr(ctx, op, err)
		}
		v, err := decode(sid, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(ctx, op, err)
	}
	return out, nil
}
