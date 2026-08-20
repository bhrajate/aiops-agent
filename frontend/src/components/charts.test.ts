import { describe, it, expect } from 'vitest'
import { trendGeometry, barRatio } from './chartMath'

// 图表数学的边界。
//
// 这里每一项的失效方式都是**渲染成空白**:SVG 的 d 属性里出现 NaN,
// 浏览器静默丢弃整条路径,不报错、控制台干净、图就是空的。
// 而"图是空的"与"窗口内确实没数据"看起来一样。

describe('trendGeometry', () => {
  it('单个桶不除以零', () => {
    // n-1 = 0 → x(i) 会算出 0/0 = NaN。
    const g = trendGeometry(1, 140)
    expect(Number.isFinite(g.x(0))).toBe(true)
  })

  it('空桶列表不产生 NaN', () => {
    const g = trendGeometry(0, 140)
    expect(Number.isFinite(g.x(0))).toBe(true)
  })

  it('全零数据时 max 至少为 1,y 不除以零', () => {
    // 全零时若 max=0,y(v) = h - (0/0)*... = NaN,整条线消失。
    const g = trendGeometry(24, 140, 0)
    expect(g.max).toBeGreaterThanOrEqual(1)
    expect(Number.isFinite(g.y(0))).toBe(true)
  })

  it('零值贴底而非居中', () => {
    // 全零的趋势线应该贴在底部,那是"没有故障"的正确视觉表达。
    // 若 max 取成 0 再兜底成 1 以外的值,零线会浮在中间,读起来像有数据。
    const g = trendGeometry(24, 140, 0)
    expect(g.y(0)).toBeGreaterThan(140 * 0.9)
  })

  it('峰值贴顶(留 4px 边距)', () => {
    const g = trendGeometry(24, 140, 10)
    expect(g.y(10)).toBeLessThan(10)
    expect(g.y(10)).toBeGreaterThanOrEqual(0)
  })
})

describe('barRatio', () => {
  it('max 为 0 时不除以零', () => {
    expect(barRatio(0, 0)).toBe(0)
  })

  it('非零值至少给 2% 宽度', () => {
    // 1 与 0 在图上必须可区分 —— "有一个"和"没有"是完全不同的信息。
    // 1/500 = 0.2%,不兜底的话那根条渲染出来是 0 像素宽。
    expect(barRatio(1, 500)).toBeGreaterThanOrEqual(2)
  })

  it('零值宽度为 0,不占视觉', () => {
    expect(barRatio(0, 500)).toBe(0)
  })

  it('最大值占满', () => {
    expect(barRatio(500, 500)).toBe(100)
  })

  it('负数(脏数据)不产生负宽度', () => {
    // 后端不该返回负计数,但 SVG 里负宽度会让整条路径失效。
    expect(barRatio(-5, 500)).toBeGreaterThanOrEqual(0)
  })
})
