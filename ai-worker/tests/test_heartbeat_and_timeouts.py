"""心跳包装与超时参数的回归测试。

覆盖两件事:
  1. ``heartbeat_while`` 必须在等待期间**持续**心跳 —— 模型调用是单次不可分割的
     await,没有天然落点。若心跳只在开始/结束发一次,一次正常但缓慢的推理会被
     Temporal 判成失联并重试,白烧一次完整的模型调用。
  2. 心跳间隔与工作流侧 heartbeat_timeout 的关系必须有余量,且各 activity 的
     超时组合要自洽。
"""
from __future__ import annotations

import asyncio

import pytest

from aiops_worker import activities as acts_mod
from aiops_worker.activities import HEARTBEAT_INTERVAL_SEC, heartbeat_while
from aiops_worker.workflow import (
    _FEEDBACK_TIMEOUT,
    _HEARTBEAT_TIMEOUT,
    _IO_TIMEOUT,
    _MODEL_TIMEOUT,
)


class _Recorder:
    """替身:记录心跳次数,并让 activity.in_activity() 返回 True。"""

    def __init__(self) -> None:
        self.beats = 0

    def in_activity(self) -> bool:
        return True

    def heartbeat(self, *details) -> None:
        self.beats += 1


@pytest.fixture
def recorder(monkeypatch):
    rec = _Recorder()
    monkeypatch.setattr(acts_mod.activity, "in_activity", rec.in_activity)
    monkeypatch.setattr(acts_mod.activity, "heartbeat", rec.heartbeat)
    return rec


@pytest.mark.asyncio
async def test_heartbeat_while_beats_during_long_await(recorder):
    """长时间等待期间必须多次心跳,而不是只在两端各一次。"""

    async def slow() -> str:
        await asyncio.sleep(0.35)
        return "done"

    # 间隔取 0.05s,0.35s 内应心跳 ~6 次(允许调度抖动)。
    result = await heartbeat_while(slow(), interval=0.05)
    assert result == "done"
    assert recorder.beats >= 4, f"心跳次数过少({recorder.beats}),长推理会被判失联"


@pytest.mark.asyncio
async def test_heartbeat_while_returns_value_without_beating_when_fast(recorder):
    """快速完成时不应产生多余心跳(避免无谓的服务端往返)。"""

    async def fast() -> int:
        return 42

    assert await heartbeat_while(fast(), interval=5.0) == 42
    assert recorder.beats == 0


@pytest.mark.asyncio
async def test_heartbeat_while_propagates_exception(recorder):
    """被包装的调用抛异常时必须原样抛出 —— 否则模型调用失败会被心跳循环吞掉。"""

    async def boom() -> None:
        raise RuntimeError("model exploded")

    with pytest.raises(RuntimeError, match="model exploded"):
        await heartbeat_while(boom(), interval=0.05)


@pytest.mark.asyncio
async def test_heartbeat_while_cancels_inner_task_on_outer_cancel(recorder):
    """外层取消(工作流取消 / activity 超时)必须传导到内层,不能留下孤儿任务
    继续消耗模型配额。"""
    started = asyncio.Event()
    cancelled = asyncio.Event()

    async def long_running() -> None:
        started.set()
        try:
            await asyncio.sleep(30)
        except asyncio.CancelledError:
            cancelled.set()
            raise

    task = asyncio.ensure_future(heartbeat_while(long_running(), interval=0.05))
    await started.wait()
    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task
    # 让取消传导完成。
    for _ in range(50):
        if cancelled.is_set():
            break
        await asyncio.sleep(0.01)
    assert cancelled.is_set(), "内层任务未被取消,会成为孤儿继续跑模型调用"


@pytest.mark.asyncio
async def test_heartbeat_while_skips_beating_outside_activity_context():
    """直接调用 activity(单测/本地脚本)时不在 activity 上下文里,
    心跳必须静默跳过而不是抛异常。"""

    async def work() -> str:
        await asyncio.sleep(0.12)
        return "ok"

    # 不打补丁:真实的 activity.in_activity() 在这里返回 False。
    assert await heartbeat_while(work(), interval=0.02) == "ok"


def test_heartbeat_interval_has_margin_under_timeout():
    """心跳间隔必须显著小于 heartbeat_timeout。

    Temporal SDK 会对心跳做节流(不是每次调用都真的上报),再加上 GC 与事件循环
    抖动,余量太小会让正常运行的 activity 被误判失联 —— 那比不设心跳更糟。
    """
    interval = HEARTBEAT_INTERVAL_SEC
    timeout = _HEARTBEAT_TIMEOUT.total_seconds()
    assert timeout / interval >= 4, (
        f"heartbeat_timeout({timeout}s)/interval({interval}s)="
        f"{timeout / interval:.1f},余量不足 4 倍"
    )


def test_heartbeat_timeout_shorter_than_model_timeout():
    """心跳超时必须明显小于 start_to_close,否则起不到「更早发现 worker 猝死」
    的作用 —— 那才是引入心跳的唯一理由。"""
    assert _HEARTBEAT_TIMEOUT < _MODEL_TIMEOUT
    assert _HEARTBEAT_TIMEOUT.total_seconds() <= _MODEL_TIMEOUT.total_seconds() / 3


def test_model_timeout_accommodates_serial_tool_calls_plus_inference():
    """run_analyzer 是**串行**工具调用后接一次模型调用。超时必须容纳最坏组合,
    否则一次正常但缓慢的分析会被判超时并重试 —— 这才是真正白烧三倍 token 的路径。

    最坏情况:单个分析器最多 2 个工具(见 ANALYZER_TOOLS 中 traces 一项),
    每次工具往返最长 AIOPS_HTTP_TIMEOUT_SEC(默认 15s),之后是 reasoning 模型。
    """
    from aiops_worker.config import Settings
    from aiops_worker.contracts import ANALYZER_TOOLS

    http_timeout = Settings().http_timeout_sec
    max_tools_per_analyzer = max(len(v) for v in ANALYZER_TOOLS.values())
    tool_budget = http_timeout * max_tools_per_analyzer
    inference_headroom = _MODEL_TIMEOUT.total_seconds() - tool_budget
    assert inference_headroom >= 120, (
        f"扣掉串行工具调用最坏 {tool_budget}s 后,只剩 {inference_headroom}s 给模型推理"
    )


def test_io_timeout_unchanged_and_below_model_timeout():
    """记账类 activity(record_phase/record_event/record_usage)不该被放宽 ——
    它们只是一次内部 HTTP 写入,超时越长意味着故障时卡得越久。"""
    assert _IO_TIMEOUT.total_seconds() == 30
    assert _IO_TIMEOUT < _MODEL_TIMEOUT


def test_feedback_timeout_is_the_wallclock_bound():
    """人工反馈等待必须远大于任何 activity 超时(它是墙钟量级),
    这条不变式是控制面 run timeout 下限的依据。"""
    assert _FEEDBACK_TIMEOUT.total_seconds() == 48 * 3600
    assert _FEEDBACK_TIMEOUT > _MODEL_TIMEOUT * 100
