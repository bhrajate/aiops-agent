// 图表的坐标换算。从 charts.tsx 抽出来是为了能直接测 ——
// 这些除法的失效方式是 SVG 路径里出现 NaN,浏览器静默丢弃整条路径:
// 不报错、控制台干净、图就是空的。而"空图"与"窗口内没数据"看起来一样。

export interface TrendGeometry {
  w: number
  h: number
  max: number
  x: (i: number) => number
  y: (v: number) => number
}

// trendGeometry 给出趋势图的坐标函数。
//
// n=1 或 0 时 (n-1) 为 0/-1,x 会算出 NaN 或负数;peak=0 时 y 会 0/0。
// 两处都要兜底,且兜底值必须让零线**贴底**(那是"没有故障"的正确视觉表达),
// 不能浮在中间 —— 浮在中间读起来像有数据。
export function trendGeometry(n: number, height: number, peak = 1): TrendGeometry {
  const w = 600
  const h = height
  // max 至少 1:全零时不能除以 0。取 1 而非其他值,让 y(0) 落在底部。
  const max = Math.max(1, peak)
  const span = n > 1 ? n - 1 : 1
  return {
    w,
    h,
    max,
    x: (i: number) => (n <= 1 ? 0 : (i / span) * w),
    // 上下各留 4px,峰值不贴死顶边
    y: (v: number) => h - (v / max) * (h - 8) - 4,
  }
}

// barRatio 返回横条的宽度百分比 [0,100]。
//
// 按**最大值**归一化而非总数:总数归一化时 20 个分类里每条都只占 5%,
// 全都短得看不出差别。
//
// 非零值兜底到 2%:1/500 = 0.2%,渲染出来是 0 像素宽,于是"有一个"和"没有"
// 在图上完全一样 —— 而那是两条不同的信息。
export function barRatio(count: number, max: number): number {
  if (max <= 0) return 0
  if (count <= 0) return 0
  const pct = (count / max) * 100
  return Math.max(pct, 2)
}
