package store

import (
	"context"
	"encoding/json"

	"github.com/aiops/control-plane/internal/model"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// ---- 人工反馈 ----

func (s *Store) InsertFeedback(ctx context.Context, fb model.Feedback) (model.Feedback, error) {
	err := s.pool.QueryRow(ctx,
		`INSERT INTO human_feedback
		 (investigation_id, author, action, confirmed_root_cause, comment, review_status)
		 VALUES ($1,$2,$3,$4,$5,'pending')
		 RETURNING feedback_id, review_status, created_at`,
		fb.InvestigationID, fb.Author, fb.Action, fb.ConfirmedRootCause, fb.Comment).
		Scan(&fb.FeedbackID, &fb.ReviewStatus, &fb.CreatedAt)
	return fb, err
}

func (s *Store) ListFeedback(ctx context.Context, invID string) ([]model.Feedback, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT feedback_id, investigation_id, author, action,
		   COALESCE(confirmed_root_cause,''), COALESCE(comment,''), review_status, created_at
		 FROM human_feedback WHERE investigation_id=$1 ORDER BY created_at`, invID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Feedback
	for rows.Next() {
		var f model.Feedback
		if err := rows.Scan(&f.FeedbackID, &f.InvestigationID, &f.Author, &f.Action,
			&f.ConfirmedRootCause, &f.Comment, &f.ReviewStatus, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---- Knowledge(retrieve_runbook 数据源;首版关键词检索,pgvector 就绪)----

type KnowledgeItem struct {
	KnowledgeID string         `json:"knowledge_id"`
	Kind        string         `json:"kind"`
	Title       string         `json:"title"`
	Content     string         `json:"content"`
	AppliesTo   map[string]any `json:"applies_to"`
	Version     string         `json:"version"`
}

// SearchKnowledge 关键词检索未失效的知识条目(区分参考知识,不作实时证据)。
func (s *Store) SearchKnowledge(ctx context.Context, q string, limit int) ([]KnowledgeItem, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	rows, err := s.pool.Query(ctx,
		`SELECT knowledge_id, kind, title, content, applies_to, COALESCE(version,'')
		 FROM knowledge_items
		 WHERE (valid_until IS NULL OR valid_until > now())
		   AND ($1='' OR title ILIKE '%'||$1||'%' OR content ILIKE '%'||$1||'%')
		 ORDER BY created_at DESC LIMIT $2`, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnowledgeItem
	for rows.Next() {
		var k KnowledgeItem
		var applies []byte
		if err := rows.Scan(&k.KnowledgeID, &k.Kind, &k.Title, &k.Content, &applies, &k.Version); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(applies, &k.AppliesTo)
		out = append(out, k)
	}
	return out, rows.Err()
}
