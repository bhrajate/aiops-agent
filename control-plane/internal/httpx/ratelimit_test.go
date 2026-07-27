package httpx

import (
	"sync"
	"testing"
	"time"
)

func fixedClock(start time.Time) (*time.Time, func() time.Time) {
	now := start
	return &now, func() time.Time { return now }
}

func TestTokenBucketDisabledWhenUnconfigured(t *testing.T) {
	for _, tc := range []struct{ rate, burst float64 }{{0, 10}, {10, 0}, {-1, -1}} {
		if tb := NewTokenBucket(tc.rate, tc.burst); tb != nil {
			t.Errorf("rate=%v burst=%v 应返回 nil(不限流)", tc.rate, tc.burst)
		}
	}
	// nil 接收者必须放行,调用方无需判空。
	var nilTB *TokenBucket
	if ok, _ := nilTB.Allow("k", 1); !ok {
		t.Error("nil 限流器应放行")
	}
	if nilTB.Len() != 0 {
		t.Error("nil 限流器 Len 应为 0")
	}
}

func TestTokenBucketAllowsBurstThenRejects(t *testing.T) {
	tb := NewTokenBucket(1, 5)
	for i := 0; i < 5; i++ {
		if ok, _ := tb.Allow("t1", 1); !ok {
			t.Fatalf("第 %d 次请求应在突发容量内被放行", i+1)
		}
	}
	ok, retry := tb.Allow("t1", 1)
	if ok {
		t.Fatal("超出突发容量后应被拒绝")
	}
	if retry <= 0 {
		t.Error("拒绝时应给出 Retry-After 建议")
	}
	// rate=1/s,缺 1 个令牌 → 约 1 秒。
	if retry > 2*time.Second {
		t.Errorf("Retry-After=%v 偏大", retry)
	}
}

func TestTokenBucketRefillsOverTime(t *testing.T) {
	now, clock := fixedClock(time.Unix(1700000000, 0))
	tb := NewTokenBucket(2, 4) // 2/s,容量 4
	tb.now = clock

	for i := 0; i < 4; i++ {
		if ok, _ := tb.Allow("t1", 1); !ok {
			t.Fatalf("第 %d 次应放行", i+1)
		}
	}
	if ok, _ := tb.Allow("t1", 1); ok {
		t.Fatal("桶已空,应拒绝")
	}

	*now = now.Add(1 * time.Second) // 补 2 个令牌
	for i := 0; i < 2; i++ {
		if ok, _ := tb.Allow("t1", 1); !ok {
			t.Fatalf("补充后第 %d 次应放行", i+1)
		}
	}
	if ok, _ := tb.Allow("t1", 1); ok {
		t.Fatal("补充的令牌用完后应再次拒绝")
	}
}

func TestTokenBucketRefillCapsAtBurst(t *testing.T) {
	now, clock := fixedClock(time.Unix(1700000000, 0))
	tb := NewTokenBucket(10, 3)
	tb.now = clock

	tb.Allow("t1", 3) // 用光
	*now = now.Add(1 * time.Hour)

	// 长时间空闲后最多补到容量,不能累积成无限配额。
	for i := 0; i < 3; i++ {
		if ok, _ := tb.Allow("t1", 1); !ok {
			t.Fatalf("第 %d 次应放行", i+1)
		}
	}
	if ok, _ := tb.Allow("t1", 1); ok {
		t.Fatal("空闲累积超过 burst:限流失效")
	}
}

func TestTokenBucketIsolatesKeys(t *testing.T) {
	tb := NewTokenBucket(1, 2)
	tb.Allow("tenant-a", 2)
	if ok, _ := tb.Allow("tenant-a", 1); ok {
		t.Fatal("tenant-a 应已耗尽")
	}
	// 一个租户打爆自己的桶不能影响别的租户。
	if ok, _ := tb.Allow("tenant-b", 1); !ok {
		t.Fatal("tenant-b 不应被 tenant-a 的流量影响")
	}
}

// 一个 Alertmanager webhook 可能带几百条告警:按条计费才挡得住风暴。
func TestTokenBucketChargesByWeight(t *testing.T) {
	tb := NewTokenBucket(1, 10)
	if ok, _ := tb.Allow("t1", 8); !ok {
		t.Fatal("8 条应在容量内")
	}
	if ok, _ := tb.Allow("t1", 5); ok {
		t.Fatal("剩余 2 个令牌,5 条应被拒绝")
	}
	if ok, _ := tb.Allow("t1", 2); !ok {
		t.Fatal("剩余刚好 2 个令牌,2 条应放行")
	}
}

// weight 超过桶容量:桶满时放行一次(不饿死),桶不满时拒绝(不可绕过)。
func TestTokenBucketOversizedBatchNeedsFullBucket(t *testing.T) {
	now, clock := fixedClock(time.Unix(1700000000, 0))
	tb := NewTokenBucket(1, 10)
	tb.now = clock

	// 桶初始为满 → 放行并清空。
	if ok, _ := tb.Allow("t1", 500); !ok {
		t.Fatal("桶满时超大批量应放行一次,否则大批量投递永远无法通过")
	}
	if ok, _ := tb.Allow("t1", 1); ok {
		t.Error("超大批量后桶应被清空")
	}

	// 关键:桶未攒满时,超大批量必须被拒——否则把风暴打包成超大批量即可绕过限流。
	*now = now.Add(5 * time.Second) // 补 5 个,未满(容量 10)
	ok, retry := tb.Allow("t1", 500)
	if ok {
		t.Fatal("桶未满却放行超大批量:限流可被超大批量绕过")
	}
	if retry <= 0 {
		t.Error("拒绝时应给出等待时间")
	}

	// 补满后可再通过一次(证明不会饿死)。
	*now = now.Add(10 * time.Second)
	if ok, _ := tb.Allow("t1", 500); !ok {
		t.Error("桶补满后超大批量应可通过")
	}
}

func TestTokenBucketWeightZeroCountsAsOne(t *testing.T) {
	tb := NewTokenBucket(1, 1)
	if ok, _ := tb.Allow("t1", 0); !ok {
		t.Fatal("weight=0 应按 1 计并放行")
	}
	if ok, _ := tb.Allow("t1", 1); ok {
		t.Error("weight=0 未扣费:可被用来绕过限流")
	}
}

func TestTokenBucketReclaimsIdleBuckets(t *testing.T) {
	now, clock := fixedClock(time.Unix(1700000000, 0))
	tb := NewTokenBucket(100, 100)
	tb.now = clock
	tb.idleTTL = time.Minute

	for i := 0; i < 50; i++ {
		tb.Allow(string(rune('a'+i%26))+string(rune('a'+i/26)), 1)
	}
	if tb.Len() == 0 {
		t.Fatal("应已创建桶")
	}
	// 超过 TTL 且已补满 → 回收(key 基数大时不至于无界占内存)。
	*now = now.Add(2 * time.Minute)
	tb.Allow("trigger-gc", 1)
	if tb.Len() > 1 {
		t.Errorf("空闲桶未回收,仍有 %d 个", tb.Len())
	}
}

func TestTokenBucketConcurrentAccessIsSafe(t *testing.T) {
	tb := NewTokenBucket(1000, 1000)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				tb.Allow("shared", 1)
				tb.Allow(string(rune('a'+n%26)), 1)
			}
		}(i)
	}
	wg.Wait() // -race 下验证无数据竞争
}
