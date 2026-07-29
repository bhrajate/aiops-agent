package store

import (
	"context"

	"github.com/aiops/control-plane/internal/model"
	"github.com/jackc/pgx/v5"
)

// InsertSignalWithOutbox 在单事务内持久化 Signal 并写 outbox(topic=signals)。
//
// 返回 inserted=false 表示该 signal_id 已存在(重复投递),**此时不写 outbox**。
//
// 为什么必须条件化:`ON CONFLICT DO NOTHING` 只保证 signals 表不出现重复行,
// 但若无条件写 outbox,重复投递仍会各自发布一次 signals 事件,Incident Manager
// 每收到一次就把 `incidents.signal_count` +1 —— 表里 1 行、计数却是 5。
// 而 signal_count 会喂给触发策略(`signal_count >= 3` 判为信号突发),于是
// **一条告警重投三次就被当成影响面扩大**,拉起不必要的深度 RCA。
// F5 的实测正是:去掉随机后缀后 signals 表已正确去重到 1 行,signal_count 仍是 5。
//
// 两者在同一事务内:enqueue 失败会连带回滚 insert,不会留下"有行无事件"。
func (s *Store) InsertSignalWithOutbox(ctx context.Context, sig model.Signal) (inserted bool, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, txErr := tx.Exec(ctx,
			`INSERT INTO signals
			 (signal_id, tenant_id, cluster_id, source, signal_type, resource_ref,
			  severity, starts_at, ends_at, labels, payload_ref, payload_hash, received_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			 ON CONFLICT (signal_id) DO NOTHING`,
			sig.SignalID, sig.TenantID, sig.ClusterID, sig.Source, sig.SignalType,
			mustJSON(sig.ResourceRef), sig.Severity, sig.StartsAt, sig.EndsAt,
			mustJSON(sig.Labels), sig.PayloadRef, sig.PayloadHash, sig.ReceivedAt)
		if txErr != nil {
			return txErr
		}
		if tag.RowsAffected() == 0 {
			// 重复投递:已有相同 signal_id,不再发布事件。
			inserted = false
			return nil
		}
		inserted = true
		return EnqueueOutboxTx(ctx, tx, "signals", sig.SignalID, sig)
	})
	if err != nil {
		return false, err
	}
	return inserted, nil
}

// AttachSignalToIncident 标记 signal 归属的 incident。
func (s *Store) AttachSignalToIncident(ctx context.Context, signalID, incidentID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE signals SET incident_id=$1 WHERE signal_id=$2`, incidentID, signalID)
	return err
}
