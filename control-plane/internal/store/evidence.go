package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/aiops/control-plane/internal/model"
	"github.com/jackc/pgx/v5"
)

func (s *Store) InsertEvidence(ctx context.Context, ev model.Evidence) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO evidence
		 (evidence_id, tenant_id, investigation_id, type, source, tool_name, query,
		  time_range, summary, raw_ref, content_hash, freshness, redaction_status)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		ev.EvidenceID, ev.TenantID, ev.InvestigationID, ev.Type, ev.Source, ev.ToolName,
		mustJSON(ev.Query), mustJSON(ev.TimeRange), ev.Summary, ev.RawRef,
		ev.ContentHash, ev.Freshness, ev.RedactionStatus)
	return err
}

const evidenceSelect = `SELECT evidence_id, tenant_id, investigation_id, type, source,
	COALESCE(tool_name,''), query, time_range, summary, COALESCE(raw_ref,''),
	content_hash, COALESCE(freshness,''), redaction_status, created_at FROM evidence`

func scanEvidence(row rowScanner) (model.Evidence, error) {
	var ev model.Evidence
	var query, timeRange []byte
	err := row.Scan(&ev.EvidenceID, &ev.TenantID, &ev.InvestigationID, &ev.Type, &ev.Source,
		&ev.ToolName, &query, &timeRange, &ev.Summary, &ev.RawRef, &ev.ContentHash,
		&ev.Freshness, &ev.RedactionStatus, &ev.CreatedAt)
	if err != nil {
		return ev, err
	}
	_ = json.Unmarshal(query, &ev.Query)
	_ = json.Unmarshal(timeRange, &ev.TimeRange)
	return ev, nil
}

func (s *Store) GetEvidence(ctx context.Context, id string) (model.Evidence, error) {
	row := s.pool.QueryRow(ctx, evidenceSelect+` WHERE evidence_id=$1`, id)
	ev, err := scanEvidence(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ev, ErrNotFound
	}
	return ev, err
}

func (s *Store) ListEvidenceByInvestigation(ctx context.Context, invID string) ([]model.Evidence, error) {
	rows, err := s.pool.Query(ctx, evidenceSelect+` WHERE investigation_id=$1 ORDER BY created_at`, invID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Evidence
	for rows.Next() {
		ev, err := scanEvidence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
