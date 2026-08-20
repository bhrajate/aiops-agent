import { describe, it, expect } from 'vitest'
import {
  TERMINAL_PHASES,
  isTerminalPhase,
  isActivePhase,
  isStalled,
  phaseLabel,
  STALL_MS,
} from './phase'
import type { InvestigationPhase } from '@/api/types'

// 契约清单:后端 control-plane/internal/api/overview.go 的 terminalPhases。
// 这份常量在这里**硬编码一份**是刻意的 —— 它就是断言本身:
// 后端改了终态集合而前端没跟,这个测试要失败。
const BACKEND_TERMINAL = [
  'closed',
  'cancelled',
  'concluded',
  'needs_human',
  'triage_published',
] as const

const ALL_PHASES: InvestigationPhase[] = [
  'queued',
  'triaging',
  'triage_published',
  'planning',
  'collecting',
  'synthesizing',
  'concluded',
  'needs_human',
  'waiting_feedback',
  'closed',
  'cancelled',
]

describe('阶段终态判定', () => {
  it('与后端 terminalPhases 逐项一致', () => {
    // 漂移的后果:总览说有 1 个调查在跑,详情页却已收到 done 并停止推流。
    // 这类矛盾不报错,只会让人不再相信面板。
    expect([...TERMINAL_PHASES].sort()).toEqual([...BACKEND_TERMINAL].sort())
  })

  it('每个阶段要么活跃要么终态,不重不漏', () => {
    for (const p of ALL_PHASES) {
      expect(isActivePhase(p)).toBe(!isTerminalPhase(p))
    }
  })

  it('waiting_feedback 算活跃,不算终态', () => {
    // 它在等人,但工作流仍然活着,run timeout 到点会被硬终止。
    // 算成终态会让"等着人却没人管"的调查从活跃计数里消失 ——
    // 而那恰恰是最需要有人去看的状态。
    expect(isActivePhase('waiting_feedback')).toBe(true)
  })

  it('每个阶段都有中文名,不回落成英文枚举', () => {
    for (const p of ALL_PHASES) {
      const label = phaseLabel(p)
      expect(label).not.toBe(p)
      expect(label.length).toBeGreaterThan(0)
    }
  })

  it('未知阶段原样返回,不抛错', () => {
    // 后端加了新阶段时界面要退化成显示英文名,而不是崩掉或显示 undefined。
    expect(phaseLabel('brand_new' as InvestigationPhase)).toBe('brand_new')
  })
})

describe('卡住判定', () => {
  const now = Date.UTC(2026, 7, 20, 12, 0, 0)
  const ago = (ms: number) => new Date(now - ms).toISOString()

  it('活跃且超过 10 分钟 → 卡住', () => {
    expect(isStalled('collecting', ago(STALL_MS + 1000), now)).toBe(true)
  })

  it('活跃但刚开始 → 不卡住', () => {
    expect(isStalled('collecting', ago(30_000), now)).toBe(false)
  })

  it('终态无论多久都不算卡住', () => {
    // 一次三天前结束的调查不该被标成"卡住" —— 它已经结束了。
    for (const p of TERMINAL_PHASES) {
      expect(isStalled(p, ago(3 * 24 * 3600_000), now)).toBe(false)
    }
  })

  it('时间戳缺失或无法解析时不判定为卡住', () => {
    // 那是数据问题。报成"卡住"会把排查方向引到 worker 上,而问题在别处。
    expect(isStalled('collecting', undefined, now)).toBe(false)
    expect(isStalled('collecting', 'not-a-date', now)).toBe(false)
  })

  it('判定线与后端 stallThreshold 一致(10 分钟)', () => {
    expect(STALL_MS).toBe(10 * 60 * 1000)
  })
})
