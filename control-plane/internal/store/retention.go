package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// 数据保留清理(F4)。设计约束:
//
//  1. **分批**:每次删除有上限,避免长事务与大范围锁把在线写入拖死。
//  2. **只删终态**:活跃 incident / 未结束调查的数据一律不动,正在进行的 RCA
//     不会因为清理而丢证据。
//  3. **先子后父**:外键约束决定删除顺序(events/evidence/hypotheses/feedback →
//     investigations → alert_groups → incidents)。
//  4. **幂等可重入**:每次只处理一批,调用方循环直到删完或达到批次上限。
//
// advisory lock 由 Janitor 持有,保证多副本下只有一个实例在清理。

// retentionLockKey 是 pg_advisory_lock 的键(任意固定常量,与其它用途区分即可)。
const retentionLockKey int64 = 0x41494F50535F4A54 // "AIOPS_JT"

// TryRetentionLock 尝试获取清理用的会话级 advisory lock。
// 返回 release 函数;未获取到锁时 ok=false(说明别的副本正在清理,本次跳过)。
func (s *Store) TryRetentionLock(ctx context.Context) (release func(), ok bool, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, retentionLockKey).Scan(&got); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !got {
		conn.Release()
		return nil, false, nil
	}
	return func() {
		// 解锁失败只影响锁的及时释放(会话结束时 PG 自动释放),不影响正确性。
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, retentionLockKey)
		conn.Release()
	}, true, nil
}

// PurgeOlderThan 按时间列分批删除一张表的过期行。
// 返回本批实际删除行数;调用方据此判断是否还需继续。
//
// table/timeCol 只允许来自内部常量表(见 retention.Table),不接受外部输入,
// 因此这里的字符串拼接不构成注入面;天数与批量走参数绑定。
func (s *Store) PurgeOlderThan(ctx context.Context, table, timeCol string, days, batch int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	q := fmt.Sprintf(`
		DELETE FROM %s
		 WHERE ctid IN (
		       SELECT ctid FROM %s
		        WHERE %s < now() - make_interval(days => $1::int)
		        LIMIT $2
		 )`, table, table, timeCol)
	tag, err := s.pool.Exec(ctx, q, days, batch)
	if err != nil {
		return 0, fmt.Errorf("purge %s: %w", table, err)
	}
	return tag.RowsAffected(), nil
}

// PurgePublishedOutbox 清理已投递的 outbox 记录(pending/failed 一律保留:
// 那是待重试的业务事件,删掉等于丢事件)。
func (s *Store) PurgePublishedOutbox(ctx context.Context, days, batch int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM outbox
		 WHERE ctid IN (
		       SELECT ctid FROM outbox
		        WHERE status = 'published'
		          AND published_at IS NOT NULL
		          AND published_at < now() - make_interval(days => $1::int)
		        LIMIT $2
		 )`, days, batch)
	if err != nil {
		return 0, fmt.Errorf("purge outbox: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PurgeOrphanSignals 清理未归属任何 incident 的过期原始信号。
// 已归属的信号随 incident 整案清理(保持案例可回溯)。
func (s *Store) PurgeOrphanSignals(ctx context.Context, days, batch int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM signals
		 WHERE ctid IN (
		       SELECT ctid FROM signals
		        WHERE incident_id IS NULL
		          AND received_at < now() - make_interval(days => $1::int)
		        LIMIT $2
		 )`, days, batch)
	if err != nil {
		return 0, fmt.Errorf("purge orphan signals: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PurgeClosedCases 整案清理:选出一批**终态且过期**的 incident,连同其
// investigations / evidence / hypotheses / events / feedback / alert_groups /
// signals 一起删除。返回删除的 incident 数与总行数。
//
// 活跃(open/acknowledged)的 incident 永不入选;非终态调查所属的 incident 也
// 会被排除——避免删掉正在跑的 RCA 的上下文。
func (s *Store) PurgeClosedCases(ctx context.Context, days, batch int) (incidents int64, rows int64, err error) {
	if days <= 0 {
		return 0, 0, nil
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		ids := make([]string, 0, batch)
		r, qerr := tx.Query(ctx, `
			SELECT incident_id FROM incidents
			 WHERE status IN ('resolved','closed')
			   AND updated_at < now() - make_interval(days => $1::int)
			   AND NOT EXISTS (
			       SELECT 1 FROM investigations iv
			        WHERE iv.incident_id = incidents.incident_id
			          AND iv.phase NOT IN ('closed','cancelled')
			   )
			 ORDER BY updated_at
			 LIMIT $2
			 FOR UPDATE SKIP LOCKED`, days, batch)
		if qerr != nil {
			return fmt.Errorf("select closed cases: %w", qerr)
		}
		for r.Next() {
			var id string
			if serr := r.Scan(&id); serr != nil {
				r.Close()
				return serr
			}
			ids = append(ids, id)
		}
		r.Close()
		if rerr := r.Err(); rerr != nil {
			return rerr
		}
		if len(ids) == 0 {
			return nil
		}

		// 先子后父:外键顺序不可颠倒。
		steps := []struct{ name, sql string }{
			{"investigation_events", `DELETE FROM investigation_events WHERE investigation_id IN (
			     SELECT investigation_id FROM investigations WHERE incident_id = ANY($1))`},
			{"human_feedback", `DELETE FROM human_feedback WHERE investigation_id IN (
			     SELECT investigation_id FROM investigations WHERE incident_id = ANY($1))`},
			{"hypotheses", `DELETE FROM hypotheses WHERE investigation_id IN (
			     SELECT investigation_id FROM investigations WHERE incident_id = ANY($1))`},
			{"evidence", `DELETE FROM evidence WHERE investigation_id IN (
			     SELECT investigation_id FROM investigations WHERE incident_id = ANY($1))`},
			{"investigations", `DELETE FROM investigations WHERE incident_id = ANY($1)`},
			{"alert_groups", `DELETE FROM alert_groups WHERE incident_id = ANY($1)`},
			{"signals", `DELETE FROM signals WHERE incident_id = ANY($1)`},
			{"incidents", `DELETE FROM incidents WHERE incident_id = ANY($1)`},
		}
		for _, st := range steps {
			tag, eerr := tx.Exec(ctx, st.sql, ids)
			if eerr != nil {
				return fmt.Errorf("purge %s: %w", st.name, eerr)
			}
			rows += tag.RowsAffected()
		}
		incidents = int64(len(ids))
		return nil
	})
	return incidents, rows, err
}
