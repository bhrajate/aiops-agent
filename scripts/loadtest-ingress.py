#!/usr/bin/env python3
"""Signal Ingress 的负载基线与护栏验证。

ACCEPTANCE 里"对接真实后端并**压测**"此前是一句待办,没有任何可跑的东西。
真实容量数字确实需要生产硬件,但有两类问题在**任何**硬件上都能验,
而它们恰好是压测真正要回答的:

  1. 限流是否**真的按配置值生效**。一个放行一切的限流器能通过所有功能测试 ——
     它只在真实告警风暴时才暴露,而那时后果是库被打满。
  2. 高并发下**幂等是否仍然成立**。串行重投递去重(check-signal-idempotency
     已覆盖)与"200 个并发同时投同一条"是不同的问题:后者会撞
     INSERT ... ON CONFLICT 的竞态窗口,而重复落库会让 signal_count 虚增。

另外顺带产出延迟分位与吞吐基线,便于日后对比回归。

用法:
  python3 scripts/loadtest-ingress.py --base http://localhost:8088 \\
      --secret webhook-dev-secret --rate 50 --duration 12
"""

import argparse
import base64
import hashlib
import hmac
import json
import statistics
import sys
import threading
import time
import urllib.error
import urllib.request
from collections import Counter


def sign(secret: str, body: bytes) -> str:
    return "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()


def post(base: str, secret: str, body: bytes, timeout: float = 10.0):
    """返回 (status, elapsed_ms, retry_after)。网络错误用 status=0 表示。"""
    req = urllib.request.Request(
        base.rstrip("/") + "/v1/signals",
        data=body,
        method="POST",
        headers={"Content-Type": "application/json", "X-AIOPS-Signature": sign(secret, body)},
    )
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, (time.perf_counter() - t0) * 1000, r.headers.get("Retry-After")
    except urllib.error.HTTPError as e:
        return e.code, (time.perf_counter() - t0) * 1000, e.headers.get("Retry-After")
    except Exception:
        return 0, (time.perf_counter() - t0) * 1000, None


def make_body(ns: str, seq: int, starts: str) -> bytes:
    """构造 Alertmanager 格式载荷。seq 参与 rule_id 使每条是不同的告警。"""
    payload = {
        "alerts": [
            {
                "status": "firing",
                "labels": {
                    "alertname": "LoadTest",
                    "severity": "warning",
                    "namespace": ns,
                    "deployment": f"svc-{seq}",
                    "cluster": "prod-cn-1",
                    "rule_id": f"lt-{seq}",
                },
                "startsAt": starts,
            }
        ]
    }
    return json.dumps(payload, separators=(",", ":")).encode()


def pct(vals, q):
    if not vals:
        return 0.0
    s = sorted(vals)
    i = min(len(s) - 1, max(0, int(len(s) * q)))
    return s[i]


def phase_throughput(base, secret, ns, rate, duration, workers):
    """以远超限流值的速率打流量,测实际放行速率与延迟。"""
    stop = time.time() + duration
    codes = Counter()
    lat = []
    retry_afters = []
    lock = threading.Lock()
    counter = [0]

    def worker():
        while time.time() < stop:
            with lock:
                counter[0] += 1
                seq = counter[0]
            starts = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
            st, ms, ra = post(base, secret, make_body(ns, seq, starts))
            with lock:
                codes[st] += 1
                lat.append(ms)
                if ra:
                    retry_afters.append(ra)

    ts = [threading.Thread(target=worker, daemon=True) for _ in range(workers)]
    t0 = time.time()
    for t in ts:
        t.start()
    for t in ts:
        t.join()
    elapsed = time.time() - t0
    return codes, lat, retry_afters, elapsed


def phase_concurrent_idempotency(base, secret, ns, n=200):
    # ns 由调用方传入独立值(见 main),使这一阶段的 incident 与吞吐阶段隔离 ——
    # 否则两者共用 correlation_key,signal_count 里混着上千条吞吐流量,
    # 无法断言"这一条只贡献了 1"。
    """n 个并发同时投**完全相同**的一条信号。

    串行重投递的去重已有覆盖,但并发是不同的问题:它会撞
    `INSERT ... ON CONFLICT DO NOTHING` 的竞态窗口。若那里有缝,
    重复行会让 incidents.signal_count 虚增 —— 而那个数喂给触发策略
    判"信号突发",于是一条告警会被读成影响面扩大。
    """
    starts = "2026-01-01T00:00:00Z"  # 固定 startsAt:同一条通知
    body = make_body(ns, 999999, starts)
    codes = Counter()
    lock = threading.Lock()

    def one():
        st, _, _ = post(base, secret, body)
        with lock:
            codes[st] += 1

    ts = [threading.Thread(target=one, daemon=True) for _ in range(n)]
    for t in ts:
        t.start()
    for t in ts:
        t.join()
    return codes


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://localhost:8088")
    ap.add_argument("--secret", default="webhook-dev-secret")
    ap.add_argument("--rate", type=float, default=50.0, help="被测实例配置的限流值")
    ap.add_argument("--duration", type=float, default=12.0)
    ap.add_argument("--workers", type=int, default=24)
    ap.add_argument("--namespace", default="loadtest")
    args = ap.parse_args()

    fails = []

    print(f"== 1) 吞吐与限流({args.duration:.0f}s,{args.workers} 并发,配置限流 {args.rate}/s)")
    codes, lat, retry_afters, elapsed = phase_throughput(
        args.base, args.secret, args.namespace, args.rate, args.duration, args.workers
    )
    total = sum(codes.values())
    accepted = codes.get(202, 0)
    throttled = codes.get(429, 0)
    neterr = codes.get(0, 0)
    acc_rate = accepted / elapsed if elapsed else 0

    print(f"   请求 {total} 条 / {elapsed:.1f}s  →  202={accepted}  429={throttled}  其他={total-accepted-throttled}")
    print(f"   放行速率 {acc_rate:.1f}/s(配置 {args.rate}/s)")
    print(f"   延迟 p50={pct(lat,0.5):.1f}ms  p95={pct(lat,0.95):.1f}ms  p99={pct(lat,0.99):.1f}ms  max={max(lat):.1f}ms")

    # 限流必须真的挡住东西。打了远超配置值的流量却一条都没被拒,
    # 说明限流没生效 —— 那在真实告警风暴时会让库被打满。
    if throttled == 0:
        fails.append("限流未生效:打了远超配置值的流量但 429 为 0")
    else:
        print("   PASS  限流生效(有 429)")

    # 放行速率不该显著超过配置值。给 3x 的宽容度:令牌桶有 burst(默认 500),
    # 且测量窗口短,起始那一桶会让均值偏高。超过 3x 说明限流形同虚设。
    if acc_rate > args.rate * 3:
        fails.append(f"放行速率 {acc_rate:.1f}/s 远超配置 {args.rate}/s(超 3x),限流形同虚设")
    else:
        print(f"   PASS  放行速率在配置值的 3x 以内")

    # 429 必须带 Retry-After,否则上游只能盲目退避。
    if throttled > 0 and not retry_afters:
        fails.append("429 响应缺 Retry-After 头,上游无法正确退避")
    elif retry_afters:
        print(f"   PASS  429 带 Retry-After(样例 {retry_afters[0]})")

    if neterr:
        fails.append(f"{neterr} 条请求网络层失败(连接被拒/超时)")

    print()
    print("== 2) 并发幂等(200 个并发投完全相同的一条信号)")
    conc_ns = args.namespace + "-conc"
    ic = phase_concurrent_idempotency(args.base, args.secret, conc_ns)
    ok2 = ic.get(202, 0)
    print(f"   202={ok2}  429={ic.get(429,0)}  其他={sum(ic.values())-ok2-ic.get(429,0)}")
    print(f"   namespace={conc_ns}(与吞吐阶段隔离,便于精确断言)")
    print("   → 落库条数与 signal_count 由调用方查库断言(见 check-loadtest.sh)")

    print()
    if fails:
        print("RESULT: FAIL")
        for f in fails:
            print("  ✗ " + f)
        sys.exit(1)
    print("RESULT: PASS(护栏在负载下成立;绝对容量数字仍需生产硬件)")


if __name__ == "__main__":
    main()
