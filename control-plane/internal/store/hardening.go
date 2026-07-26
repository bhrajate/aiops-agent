package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ---- 幂等键(SECURITY §5)----

// GetIdempotentResult 返回已记录的结果 ID(存在则 found=true)。
func (s *Store) GetIdempotentResult(ctx context.Context, key string) (resultID string, found bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT result_id FROM idempotency_keys WHERE key=$1`, key).Scan(&resultID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return resultID, true, nil
}

// PutIdempotentResult 记录 key→result 映射;若并发已存在,返回既有值。
func (s *Store) PutIdempotentResult(ctx context.Context, key, scope, targetID, resultID string) (string, error) {
	var stored string
	err := s.pool.QueryRow(ctx,
		`INSERT INTO idempotency_keys (key, scope, target_id, result_id)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (key) DO UPDATE SET key=EXCLUDED.key
		 RETURNING result_id`,
		key, scope, targetID, resultID).Scan(&stored)
	return stored, err
}

// ---- 死信队列(SECURITY §7)----

func (s *Store) InsertDeadLetter(ctx context.Context, topic, key string, payload []byte, errMsg string, attempts int) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO dead_letters (topic, key, payload, error, attempts) VALUES ($1,$2,$3,$4,$5)`,
		topic, key, payload, errMsg, attempts)
	return err
}

// CountDeadLetters 供指标/健康检查。
func (s *Store) CountDeadLetters(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM dead_letters`).Scan(&n)
	return n, err
}
