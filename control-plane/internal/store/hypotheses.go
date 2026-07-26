package store

import (
	"context"
	"encoding/json"

	"github.com/aiops/control-plane/internal/model"
	"github.com/jackc/pgx/v5"
)

// ReplaceHypotheses 全量替换某调查的假设集合(Synthesizer 每轮重算)。
func (s *Store) ReplaceHypotheses(ctx context.Context, invID string, hyps []model.Hypothesis) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM hypotheses WHERE investigation_id=$1`, invID); err != nil {
			return err
		}
		for _, h := range hyps {
			_, err := tx.Exec(ctx,
				`INSERT INTO hypotheses
				 (hypothesis_id, tenant_id, investigation_id, rank, statement, component_ref,
				  confidence, supporting_evidence_ids, contradicting_evidence_ids,
				  missing_evidence, status)
				 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
				h.HypothesisID, orDefault(h.HypothesisID), invID, h.Rank, h.Statement,
				mustJSON(h.ComponentRef), h.Confidence,
				mustJSON(h.SupportingEvidenceIDs), mustJSON(h.ContradictingEvidenceIDs),
				mustJSON(h.MissingEvidence), h.Status)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// orDefault 占位:tenant 固定 default(hypothesis_id 已由调用方给出)。
func orDefault(_ string) string { return "default" }

func (s *Store) ListHypotheses(ctx context.Context, invID string) ([]model.Hypothesis, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT hypothesis_id, investigation_id, COALESCE(rank,0), statement, component_ref,
		   confidence, supporting_evidence_ids, contradicting_evidence_ids, missing_evidence, status
		 FROM hypotheses WHERE investigation_id=$1 ORDER BY rank`, invID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Hypothesis
	for rows.Next() {
		var h model.Hypothesis
		var comp, sup, con, miss []byte
		if err := rows.Scan(&h.HypothesisID, &h.InvestigationID, &h.Rank, &h.Statement,
			&comp, &h.Confidence, &sup, &con, &miss, &h.Status); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(comp, &h.ComponentRef)
		_ = json.Unmarshal(sup, &h.SupportingEvidenceIDs)
		_ = json.Unmarshal(con, &h.ContradictingEvidenceIDs)
		_ = json.Unmarshal(miss, &h.MissingEvidence)
		out = append(out, h)
	}
	return out, rows.Err()
}
