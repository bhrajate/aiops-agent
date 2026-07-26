package store

import (
	"context"
	"errors"

	"github.com/aiops/control-plane/internal/model"
	"github.com/jackc/pgx/v5/pgconn"
)

// AppendEvent 追加一条时间线事件,seq 自动递增。返回该事件 seq。
// seq 由 max(seq)+1 计算,并发 append 会撞 UNIQUE(investigation_id, seq),
// 因此对唯一冲突做有界重试,避免时间线事件在并发下静默丢失。
func (s *Store) AppendEvent(ctx context.Context, invID, eventType string, payload map[string]any) (int, error) {
	const maxRetries = 5
	body := mustJSON(payload)
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		var seq int
		err := s.pool.QueryRow(ctx,
			`INSERT INTO investigation_events (investigation_id, seq, event_type, payload)
			 VALUES ($1,
			   COALESCE((SELECT max(seq)+1 FROM investigation_events WHERE investigation_id=$1), 1),
			   $2, $3)
			 RETURNING seq`,
			invID, eventType, body).Scan(&seq)
		if err == nil {
			return seq, nil
		}
		lastErr = err
		if !isUniqueViolation(err) {
			return 0, err // 非并发冲突,直接返回
		}
		// 冲突:另一并发写抢占了该 seq,重算重试
	}
	return 0, lastErr
}

// isUniqueViolation 判断是否 PostgreSQL 唯一约束冲突(SQLSTATE 23505)。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// EventsSince 拉取 seq 之后的事件(供 SSE 增量推送与回放)。
func (s *Store) EventsSince(ctx context.Context, invID string, afterSeq int) ([]model.InvestigationEvent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, investigation_id, seq, event_type, payload, created_at
		 FROM investigation_events
		 WHERE investigation_id=$1 AND seq>$2 ORDER BY seq`, invID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.InvestigationEvent
	for rows.Next() {
		var e model.InvestigationEvent
		var payload []byte
		if err := rows.Scan(&e.ID, &e.InvestigationID, &e.Seq, &e.EventType, &payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = jsonUnmarshal(payload, &e.Payload)
		out = append(out, e)
	}
	return out, rows.Err()
}
