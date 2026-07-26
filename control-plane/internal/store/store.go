// Package store 封装业务库(PostgreSQL)访问。业务库是事实源。
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

// New 创建连接池并 ping。
func New(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("new pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Health 简单健康检查。
func (s *Store) Health(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.pool.Ping(c)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}

// ---- Outbox Pattern(文档 13):在业务事务内写入,后台投递 ----

// EnqueueOutboxTx 在给定事务内写 outbox 记录。
func EnqueueOutboxTx(ctx context.Context, tx pgx.Tx, topic, key string, payload any) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO outbox (topic, key, payload) VALUES ($1,$2,$3)`,
		topic, key, mustJSON(payload))
	return err
}

// OutboxRow 待投递记录。
type OutboxRow struct {
	ID      int64
	Topic   string
	Key     string
	Payload []byte
}

// FetchPendingOutbox 取一批待投递记录:pending,或 failed 但重试未超上限的(可重投)。
// maxAttempts<=0 表示 failed 不重投(仅 pending)。
func (s *Store) FetchPendingOutbox(ctx context.Context, limit, maxAttempts int) ([]OutboxRow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, topic, key, payload FROM outbox
		 WHERE status='pending' OR (status='failed' AND attempts < $2)
		 ORDER BY id LIMIT $1`, limit, maxAttempts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxRow
	for rows.Next() {
		var r OutboxRow
		if err := rows.Scan(&r.ID, &r.Topic, &r.Key, &r.Payload); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) MarkOutboxPublished(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE outbox SET status='published', published_at=now() WHERE id=$1`, id)
	return err
}

// MarkOutboxFailed 递增 attempts 并置 failed,返回新的 attempts 值(供上限判断)。
func (s *Store) MarkOutboxFailed(ctx context.Context, id int64) (int, error) {
	var attempts int
	err := s.pool.QueryRow(ctx,
		`UPDATE outbox SET status='failed', attempts=attempts+1 WHERE id=$1 RETURNING attempts`, id).
		Scan(&attempts)
	return attempts, err
}

// MarkOutboxDead 将超过重试上限的记录标记为 dead(不再被 FetchPendingOutbox 取回)。
func (s *Store) MarkOutboxDead(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE outbox SET status='dead' WHERE id=$1`, id)
	return err
}

// ---- 审计(文档 14.3) ----

func (s *Store) Audit(ctx context.Context, tenant, actor, action, targetType, targetID, result string, scope, detail any) {
	_, _ = s.pool.Exec(ctx,
		`INSERT INTO audit_log (tenant_id, actor, action, target_type, target_id, scope, result, detail)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		tenant, actor, action, targetType, targetID, mustJSON(scope), result, mustJSON(detail))
}
