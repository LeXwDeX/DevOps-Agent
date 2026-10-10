package pgstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	schema "github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/core"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// eventRow 是一条已校验、已编码、可以直接写入 outbox 的事件。
type eventRow struct {
	id, stream string
	seq        int64
	typ        string
	at         time.Time
	raw        []byte
}

// prepareEvents 在开启事务之前校验并编码事件，所以格式错误的事件不会碰数据库。
func prepareEvents(evts []events.Event) ([]eventRow, error) {
	rows := make([]eventRow, 0, len(evts))
	for _, e := range evts {
		if err := e.Validate(); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(eventToDoc(e))
		if err != nil {
			return nil, errcode.Wrap(errcode.InvalidArgument, "event "+e.EventID+" cannot be encoded", err)
		}
		rows = append(rows, eventRow{id: e.EventID, stream: e.StreamID, seq: e.Sequence, typ: string(e.Type), at: ut(e.OccurredAt), raw: raw})
	}
	return rows, nil
}

// insertEvents 在事务里写入事件。event_id 或 (stream_id, sequence) 重复是调用方的错误：
// core.invalid_argument，并让整个事务回滚。
func insertEvents(ctx context.Context, tx pgx.Tx, rows []eventRow) error {
	for _, r := range rows {
		_, err := tx.Exec(ctx, sqlOutboxInsert, r.id, r.stream, r.seq, r.typ, r.at, schema.EventV1, r.raw)
		if err == nil {
			continue
		}
		if isConstraint(err, pgUniqueViolation, conOutboxEventID, conOutboxStreamSeq) {
			return errcode.Newf(errcode.InvalidArgument, "duplicate event %s (stream %s, sequence %d)", r.id, r.stream, r.seq)
		}
		return mapErr(ctx, "insert event", err)
	}
	return nil
}

// decodeEvent 解码 outbox 里的一行。
func decodeEvent(schemaID string, raw []byte) (events.Event, error) {
	var doc schema.EventDocV1
	if err := decodeStored("event", schemaID, schema.EventV1, raw, &doc); err != nil {
		return events.Event{}, err
	}
	return eventFromDoc(doc)
}
