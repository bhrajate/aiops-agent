package store

import (
	"context"

	"github.com/aiops/control-plane/internal/model"
)

// AppendEvent 追加一条时间线事件,seq 自动递增。返回该事件 seq。
func (s *Store) AppendEvent(ctx context.Context, invID, eventType string, payload map[string]any) (int, error) {
	var seq int
	err := s.pool.QueryRow(ctx,
		`INSERT INTO investigation_events (investigation_id, seq, event_type, payload)
		 VALUES ($1,
		   COALESCE((SELECT max(seq)+1 FROM investigation_events WHERE investigation_id=$1), 1),
		   $2, $3)
		 RETURNING seq`,
		invID, eventType, mustJSON(payload)).Scan(&seq)
	return seq, err
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
