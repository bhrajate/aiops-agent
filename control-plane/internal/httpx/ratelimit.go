package httpx

import (
	"sync"
	"time"
)

// RateLimiter 抽象限流(便于替身测试与后续换实现)。
type RateLimiter interface {
	// Allow 尝试为 key 扣除 weight 个令牌。
	// 返回是否放行,以及建议的重试等待时间(用于 Retry-After)。
	Allow(key string, weight int) (bool, time.Duration)
}

// TokenBucket 是**进程内**的分键令牌桶。
//
// 为什么进程内而不是 Redis:限流的目的是挡住告警风暴打穿 ingress/DB/outbox,
// 不需要全局精确配额。每副本独立配额意味着总容量随副本数线性放大——这是明确的
// 取舍:换来不引入新的故障点(Redis 挂了不能让信号入口跟着挂)。
// 若将来需要全局精确配额,换成 Redis 实现即可,接口不变。
//
// 按需补充令牌(不开后台 goroutine):每次访问按时间差补齐,空闲桶由 sweep 回收,
// 因此 key 基数大(多租户)也不会无界占内存。
type TokenBucket struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	rate    float64 // 每秒补充令牌数
	burst   float64 // 桶容量(允许的瞬时突发)
	idleTTL time.Duration
	now     func() time.Time // 可注入,便于测试
	lastGC  time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewTokenBucket 创建限流器。ratePerSec<=0 或 burst<=0 表示不限流(返回 nil)。
func NewTokenBucket(ratePerSec, burst float64) *TokenBucket {
	if ratePerSec <= 0 || burst <= 0 {
		return nil
	}
	return &TokenBucket{
		buckets: map[string]*bucket{},
		rate:    ratePerSec,
		burst:   burst,
		idleTTL: 10 * time.Minute,
		now:     time.Now,
	}
}

// Allow 扣除 weight 个令牌。weight<=0 视为 1。
// nil 接收者放行——未配置限流时调用方无需判空。
func (t *TokenBucket) Allow(key string, weight int) (bool, time.Duration) {
	if t == nil {
		return true, 0
	}
	if weight <= 0 {
		weight = 1
	}
	w := float64(weight)

	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.gcLocked(now)

	b, ok := t.buckets[key]
	if !ok {
		b = &bucket{tokens: t.burst, last: now}
		t.buckets[key] = b
	} else {
		// 按时间差补充,上限为桶容量。
		if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
			b.tokens += elapsed * t.rate
			if b.tokens > t.burst {
				b.tokens = t.burst
			}
		}
		b.last = now
	}

	// 单次请求的 weight 可能超过桶容量(如 burst=500 却来了 501 条告警)。
	// 直接拒绝会永久丢弃这类投递;直接放行则等于留了个后门——把风暴打包成
	// 超大批量即可完全绕过限流。折中:**仅当桶已攒满时**放行一次并清空。
	//
	// 这样两个性质同时成立:
	//   - 不饿死:桶总会补满,超大批量最终一定能通过;
	//   - 不可绕过:连续超大批量之间必须等桶重新攒满,长期速率仍受 rate 约束。
	if w > t.burst {
		if b.tokens >= t.burst {
			b.tokens = 0
			return true, 0
		}
		need := t.burst - b.tokens
		return false, time.Duration(need / t.rate * float64(time.Second))
	}
	if b.tokens >= w {
		b.tokens -= w
		return true, 0
	}
	// 还差多少令牌 → 需要等多久。
	need := w - b.tokens
	return false, time.Duration(need / t.rate * float64(time.Second))
}

// gcLocked 周期性回收空闲桶,避免 key 基数增长导致内存泄漏。
func (t *TokenBucket) gcLocked(now time.Time) {
	if now.Sub(t.lastGC) < t.idleTTL {
		return
	}
	t.lastGC = now
	for k, b := range t.buckets {
		idle := now.Sub(b.last)
		if idle <= t.idleTTL {
			continue
		}
		// 判据是"若此刻访问会不会已经补满",而不是桶里当前的 tokens ——
		// 空闲桶的 tokens 是过期快照(补充是惰性的),用它判断会导致永不回收。
		// 已能补满的桶没有状态可保留,删除等价于按需重建。
		if b.tokens+idle.Seconds()*t.rate >= t.burst {
			delete(t.buckets, k)
		}
	}
}

// Len 返回当前桶数量(测试与观测用)。
func (t *TokenBucket) Len() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.buckets)
}
