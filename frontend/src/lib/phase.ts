// 调查阶段的判定与中文名。
//
// 单独成模块(而不是留在 Badges.tsx)是为了让它能被直接测试与复用:
// Badges.tsx 里带 JSX,测试它需要拉进 React 渲染栈;而"哪些阶段算进行中"
// 是纯逻辑,且**必须与后端两处保持一致**:
//   control-plane/internal/api/overview.go   terminalPhases
//   control-plane/internal/api/sse_feedback.go  isTerminal
// 后端已有测试断言那两处不漂移(TestTerminalPhasesMatchesIsTerminal),
// 这里补上前端与它们的一致性断言。
//
// 漂移的后果:总览说有 1 个调查在跑,详情页却已经收到 done 事件并停止推流;
// 或者列表的"进行中"筛选与总览的活跃计数对不上。这类矛盾不会报错,
// 只会让人不再相信面板。

import type { InvestigationPhase } from '@/api/types'

// 终态阶段。与后端 terminalPhases 逐项对应。
//
// 注意 waiting_feedback **不在**终态里:它在等人,但工作流仍然活着,
// run timeout 到点会被硬终止。把它算成终态会让"等着人却没人管"的调查
// 从活跃计数里消失 —— 而那恰恰是需要有人去看的状态。
export const TERMINAL_PHASES: readonly InvestigationPhase[] = [
  'closed',
  'cancelled',
  'concluded',
  'needs_human',
  'triage_published',
]

export function isTerminalPhase(phase: InvestigationPhase): boolean {
  return TERMINAL_PHASES.includes(phase)
}

export function isActivePhase(phase: InvestigationPhase): boolean {
  return !isTerminalPhase(phase)
}

// "卡住"判定线。
//
// 与后端 overview.go 的 stallThreshold 一致(10 分钟)。默认预算是 300 秒,
// 这里取 2 倍并设 10 分钟下限:判定太紧会把正常的长调查标成卡住,
// 而误报几次之后没人再看这个数字。
export const STALL_MS = 10 * 60 * 1000

// stalledSince 判断一次调查是否疑似卡住。
//
// 用**挂钟时间**而不是 usage.elapsed_sec:后者由 worker 上报,worker 挂了
// 它就不再更新 —— 而那正是要检测的情况。
export function isStalled(
  phase: InvestigationPhase,
  startedAt: string | undefined,
  now: number = Date.now(),
): boolean {
  if (!isActivePhase(phase)) return false
  if (!startedAt) return false
  const t = new Date(startedAt).getTime()
  // 时间戳无法解析时不判定为卡住:那是数据问题,报成"卡住"会误导排查方向。
  if (!Number.isFinite(t)) return false
  return now - t > STALL_MS
}

const PHASE_LABEL: Record<InvestigationPhase, string> = {
  queued: '排队中',
  triaging: '分诊中',
  triage_published: '分诊已发布',
  planning: '规划中',
  collecting: '证据采集中',
  synthesizing: '综合分析中',
  concluded: '已得出结论',
  needs_human: '需人工介入',
  waiting_feedback: '等待反馈',
  closed: '已关闭',
  cancelled: '已取消',
}

export function phaseLabel(phase: InvestigationPhase): string {
  return PHASE_LABEL[phase] ?? phase
}
