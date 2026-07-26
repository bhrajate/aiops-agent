package store

import (
	"context"

	"github.com/aiops/control-plane/internal/model"
	"github.com/jackc/pgx/v5"
)

// InsertSignalWithOutbox 在单事务内持久化 Signal 并写 outbox(topic=signals)。
func (s *Store) InsertSignalWithOutbox(ctx context.Context, sig model.Signal) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO signals
			 (signal_id, tenant_id, cluster_id, source, signal_type, resource_ref,
			  severity, starts_at, ends_at, labels, payload_ref, payload_hash, received_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			 ON CONFLICT (signal_id) DO NOTHING`,
			sig.SignalID, sig.TenantID, sig.ClusterID, sig.Source, sig.SignalType,
			mustJSON(sig.ResourceRef), sig.Severity, sig.StartsAt, sig.EndsAt,
			mustJSON(sig.Labels), sig.PayloadRef, sig.PayloadHash, sig.ReceivedAt)
		if err != nil {
			return err
		}
		return EnqueueOutboxTx(ctx, tx, "signals", sig.SignalID, sig)
	})
}

// AttachSignalToIncident 标记 signal 归属的 incident。
func (s *Store) AttachSignalToIncident(ctx context.Context, signalID, incidentID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE signals SET incident_id=$1 WHERE signal_id=$2`, incidentID, signalID)
	return err
}
