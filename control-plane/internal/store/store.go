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

// PublishFunc 将一条 outbox 记录发布到事件总线;返回 error 表示发布失败(将重试/进 dead)。
type PublishFunc func(ctx context.Context, topic, key string, payload []byte) error

// DrainOutbox 在单个事务内完成 取(加锁)→ 发布 → 标记,保证 FOR UPDATE SKIP LOCKED
// 的行锁在整个批次期间持有 —— 多副本并发时同一行只会被一个副本处理(A1 修复)。
// 返回本批处理的记录数。
func (s *Store) DrainOutbox(ctx context.Context, limit, maxAttempts int, publish PublishFunc, log func(msg string, kv ...any)) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // 提交后 rollback 为 no-op

	rows, err := tx.Query(ctx,
		`SELECT id, topic, key, payload FROM outbox
		 WHERE status='pending' OR (status='failed' AND attempts < $2)
		 ORDER BY id LIMIT $1
		 FOR UPDATE SKIP LOCKED`, limit, maxAttempts)
	if err != nil {
		return 0, err
	}
	var batch []OutboxRow
	for rows.Next() {
		var r OutboxRow
		if err := rows.Scan(&r.ID, &r.Topic, &r.Key, &r.Payload); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, r := range batch {
		if perr := publish(ctx, r.Topic, r.Key, r.Payload); perr != nil {
			var attempts int
			if e := tx.QueryRow(ctx,
				`UPDATE outbox SET status='failed', attempts=attempts+1 WHERE id=$1 RETURNING attempts`, r.ID).
				Scan(&attempts); e != nil {
				return 0, e
			}
			if attempts >= maxAttempts {
				if _, e := tx.Exec(ctx, `UPDATE outbox SET status='dead' WHERE id=$1`, r.ID); e != nil {
					return 0, e
				}
				if log != nil {
					log("outbox record dead after max attempts", "id", r.ID, "topic", r.Topic, "attempts", attempts, "err", perr)
				}
			} else if log != nil {
				log("publish outbox failed (will retry)", "id", r.ID, "topic", r.Topic, "attempts", attempts, "err", perr)
			}
			continue
		}
		if _, e := tx.Exec(ctx,
			`UPDATE outbox SET status='published', published_at=now() WHERE id=$1`, r.ID); e != nil {
			return 0, e
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(batch), nil
}

// ---- 审计(文档 14.3) ----

func (s *Store) Audit(ctx context.Context, tenant, actor, action, targetType, targetID, result string, scope, detail any) {
	_, _ = s.pool.Exec(ctx,
		`INSERT INTO audit_log (tenant_id, actor, action, target_type, target_id, scope, result, detail)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		tenant, actor, action, targetType, targetID, mustJSON(scope), result, mustJSON(detail))
}
