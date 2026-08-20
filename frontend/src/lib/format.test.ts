import { describe, it, expect } from 'vitest'
import {
  formatCost,
  formatDuration,
  formatTokens,
  formatBlastRadius,
  formatResource,
  relativeTime,
  actionLabel,
  cn,
} from './format'

describe('formatCost', () => {
  it('小额成本保留 4 位,不塌成 $0.00', () => {
    // $0.0031 与 $0.00 是不同的信息:后者会被读成"没花钱",
    // 而累计成本的分析恰恰依赖这些小额。
    expect(formatCost(0.0031)).toBe('$0.0031')
    expect(formatCost(0.00004)).toBe('$0.0000')
  })

  it('0 与 undefined 都显示 $0.00', () => {
    expect(formatCost(0)).toBe('$0.00')
    expect(formatCost(undefined)).toBe('$0.00')
  })

  it('常规金额两位小数', () => {
    expect(formatCost(1.5)).toBe('$1.50')
    expect(formatCost(12.345)).toBe('$12.35')
  })
})

describe('formatDuration', () => {
  it('null / undefined 显示破折号,不显示 0s', () => {
    // 关键契约:后端在样本不足时返回 null(mttr_seconds / p95)。
    // 显示成 "0s" 会被读成"秒级解决",而真相是没有样本。
    expect(formatDuration(null)).toBe('—')
    expect(formatDuration(undefined)).toBe('—')
  })

  it('0 与亚秒级区分于"无数据"', () => {
    expect(formatDuration(0)).toBe('<1s')
    expect(formatDuration(0.4)).toBe('<1s')
  })

  it('逐级收敛到 s / m / h / d', () => {
    expect(formatDuration(45)).toBe('45s')
    expect(formatDuration(90)).toBe('1m 30s')
    expect(formatDuration(120)).toBe('2m')
    expect(formatDuration(3600)).toBe('1h')
    expect(formatDuration(3900)).toBe('1h 5m')
    expect(formatDuration(90000)).toBe('1d 1h')
  })
})

describe('formatTokens', () => {
  it('大数收敛成 k / M', () => {
    expect(formatTokens(0)).toBe('0')
    expect(formatTokens(999)).toBe('999')
    expect(formatTokens(1500)).toBe('1.5k')
    expect(formatTokens(200000)).toBe('200k')
    expect(formatTokens(2_500_000)).toBe('2.5M')
  })
})

describe('formatBlastRadius', () => {
  it('服务数是主口径,资源数仅在不同时补充', () => {
    // 单服务多 Pod 不应显示成多个服务 —— 影响面会虚高一个量级,
    // 而它驱动深度 RCA 的闸门。
    expect(formatBlastRadius({ namespaces: 1, services: 1, resources: 1 })).toBe(
      '1 服务 / 1 命名空间',
    )
    expect(formatBlastRadius({ namespaces: 1, services: 1, resources: 5 })).toBe(
      '1 服务 / 5 资源 / 1 命名空间',
    )
  })

  it('缺失时显示破折号而不是 0', () => {
    expect(formatBlastRadius(undefined)).toBe('—')
    expect(formatBlastRadius({} as never)).toBe('—')
  })

  it('0 服务是有效值,要显示出来', () => {
    // "0 服务"与"影响面未知"是不同的:前者说明聚合算过了。
    expect(formatBlastRadius({ namespaces: 0, services: 0 })).toBe(
      '0 服务 / 0 命名空间',
    )
  })
})

describe('formatResource', () => {
  it('拼成 Kind/Name', () => {
    expect(formatResource({ kind: 'Deployment', name: 'checkout' })).toBe(
      'Deployment/checkout',
    )
  })

  it('缺字段时降级而非输出 undefined', () => {
    expect(formatResource({ name: 'checkout' })).toBe('checkout')
    expect(formatResource({ kind: 'Pod' })).toBe('Pod')
    expect(formatResource(undefined)).toBe('—')
    expect(formatResource({})).toBe('—')
  })
})

describe('relativeTime', () => {
  it('未来时间不显示成负数', () => {
    // 客户端与服务端时钟有偏差时,"-3 秒前"会让人以为数据坏了。
    const future = new Date(Date.now() + 60_000).toISOString()
    expect(relativeTime(future)).toBe('刚刚')
  })

  it('无输入或非法输入不抛错', () => {
    expect(relativeTime(undefined)).toBe('—')
    expect(relativeTime('garbage')).toBe('garbage')
  })

  it('按量级选单位', () => {
    const ago = (ms: number) => new Date(Date.now() - ms).toISOString()
    expect(relativeTime(ago(5_000))).toMatch(/秒前$/)
    expect(relativeTime(ago(5 * 60_000))).toMatch(/分钟前$/)
    expect(relativeTime(ago(5 * 3600_000))).toMatch(/小时前$/)
    expect(relativeTime(ago(5 * 24 * 3600_000))).toMatch(/天前$/)
    expect(relativeTime(ago(90 * 24 * 3600_000))).toMatch(/个月前$/)
  })
})

describe('actionLabel', () => {
  it('已知动作译成中文', () => {
    expect(actionLabel('human_feedback')).toBe('人工反馈')
    expect(actionLabel('incident_status_change')).toBe('变更状态')
  })

  it('未知动作原样返回,不掩盖新枚举', () => {
    // 后端加了新审计动作时,界面显示英文名比显示空白或"未知操作"有用 ——
    // 至少能搜到它在代码里哪写的。
    expect(actionLabel('some_new_action')).toBe('some_new_action')
  })
})

describe('cn', () => {
  it('过滤假值', () => {
    expect(cn('a', false, null, undefined, 'b')).toBe('a b')
    expect(cn()).toBe('')
  })
})
