import { useId, useMemo, useState } from 'react'
import type { CountPair, TrendBucket } from '@/api/types'
import { cn, formatClock, formatCount } from '@/lib/format'
import { trendGeometry, barRatio } from './chartMath'

// 手写 SVG 图表而不是引入 recharts/echarts。
//
// 理由是体积:总览只需要一个面积图和几条横条,recharts 会往 bundle 里加
// ~100KB(gzip 后仍 ~35KB),而值班台常在带宽受限的跳板机上打开。
// 这几个组件加起来不到 200 行,且完全可控。

// ── 横向条形分布 ────────────────────────────────────────
// 用于级别 / 状态 / 故障类别 / 阶段分布。
//
// 刻意用横条而非饼图:饼图在 5 个以上分类时无法比较相邻扇区大小,
// 而"P2 比 P3 多多少"恰恰是值班时要判断的。横条按长度直接可比。
export function BarDistribution({
  data,
  total: totalProp,
  colorOf,
  labelOf,
  onSelect,
  emptyHint = '窗口内无数据',
}: {
  data: CountPair[] | null | undefined
  // 显式总数(用于算百分比)。不传则用各项之和。
  total?: number
  colorOf?: (key: string) => string
  labelOf?: (key: string) => string
  onSelect?: (key: string) => void
  emptyHint?: string
}) {
  const items = data ?? []
  const sum = items.reduce((a, b) => a + b.count, 0)
  const total = totalProp && totalProp > 0 ? totalProp : sum
  // 条长按**最大值**归一化而非按总数:总数归一化时,20 个分类里
  // 每条都只占 5%,全都短得看不出差别。
  const max = items.reduce((a, b) => Math.max(a, b.count), 0)

  if (items.length === 0) {
    return <p className="px-1 py-6 text-center text-xs text-faint">{emptyHint}</p>
  }

  return (
    <ul className="space-y-2">
      {items.map((it) => {
        // 宽度换算走 chartMath.barRatio(有测试钉住边界)。
        // 不在这里内联算:那会让同一个公式有两份实现,而两份迟早漂移。
        const widthPct = barRatio(it.count, max)
        const share = total > 0 ? it.count / total : 0
        const Tag = onSelect ? 'button' : 'div'
        return (
          <li key={it.key}>
            <Tag
              onClick={onSelect ? () => onSelect(it.key) : undefined}
              className={cn(
                'block w-full text-left',
                onSelect && 'group cursor-pointer',
              )}
            >
              <div className="flex items-baseline justify-between gap-2 text-xs">
                <span
                  className={cn(
                    'truncate text-muted',
                    onSelect && 'group-hover:text-content',
                  )}
                >
                  {labelOf ? labelOf(it.key) : it.key}
                </span>
                <span className="tabular shrink-0 text-faint">
                  {formatCount(it.count)}
                  <span className="ml-1 text-2xs">
                    {share > 0 ? `${Math.round(share * 100)}%` : ''}
                  </span>
                </span>
              </div>
              <div className="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-card-soft">
                <div
                  className={cn(
                    'h-full rounded-full transition-all',
                    colorOf?.(it.key) ?? 'bg-accent',
                  )}
                  style={{ width: `${widthPct}%` }}
                />
              </div>
            </Tag>
          </li>
        )
      })}
    </ul>
  )
}

// ── 趋势面积图 ──────────────────────────────────────────
// 新增 / 已解决 / 调查三条线。
export function TrendChart({
  buckets,
  height = 140,
}: {
  buckets: TrendBucket[]
  height?: number
}) {
  // useId 返回形如 ":R0:" 的串。浏览器能把 url(#:R0:) 当 URI 片段解析,
  // 但冒号让它无法用 querySelector('#...') 选中(需转义),排查时很别扭。
  // 去掉冒号成本为零,顺手做掉。
  const gradId = `grad-${useId().replace(/:/g, '')}`
  const [hover, setHover] = useState<number | null>(null)

  const { paths, max, w, h } = useMemo(() => {
    const n = buckets.length
    const peak = buckets.length
      ? Math.max(...buckets.map((b) => Math.max(b.new, b.resolved, b.investigations)))
      : 0
    // 坐标换算走 chartMath.trendGeometry(有测试钉住 n≤1 与全零两个除零边界)。
    const { w, h, max, x, y } = trendGeometry(n, height, peak)

    function line(get: (b: TrendBucket) => number): string {
      if (n === 0) return ''
      return buckets
        .map((b, i) => `${i === 0 ? 'M' : 'L'}${x(i).toFixed(1)},${y(get(b)).toFixed(1)}`)
        .join(' ')
    }
    function area(get: (b: TrendBucket) => number): string {
      if (n === 0) return ''
      return `${line(get)} L${w},${h} L0,${h} Z`
    }
    return {
      w,
      h,
      max,
      paths: {
        newLine: line((b) => b.new),
        newArea: area((b) => b.new),
        resolved: line((b) => b.resolved),
        investigations: line((b) => b.investigations),
      },
    }
  }, [buckets, height])

  if (buckets.length === 0) {
    return (
      <p className="py-8 text-center text-xs text-faint">窗口内无趋势数据</p>
    )
  }

  const hoveredBucket = hover != null ? buckets[hover] : null

  return (
    <div className="relative">
      <svg
        viewBox={`0 0 ${w} ${h}`}
        preserveAspectRatio="none"
        className="h-[140px] w-full"
        role="img"
        aria-label="故障与调查趋势"
        onMouseLeave={() => setHover(null)}
        onMouseMove={(e) => {
          const rect = e.currentTarget.getBoundingClientRect()
          const ratio = (e.clientX - rect.left) / rect.width
          const idx = Math.round(ratio * (buckets.length - 1))
          setHover(Math.min(buckets.length - 1, Math.max(0, idx)))
        }}
      >
        <defs>
          <linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
            <stop
              offset="0%"
              stopColor="rgb(var(--danger))"
              stopOpacity="0.28"
            />
            <stop
              offset="100%"
              stopColor="rgb(var(--danger))"
              stopOpacity="0"
            />
          </linearGradient>
        </defs>
        {/* 网格线:4 等分,给读数一个参照 */}
        {[0.25, 0.5, 0.75].map((f) => (
          <line
            key={f}
            x1="0"
            x2={w}
            y1={h * f}
            y2={h * f}
            stroke="rgb(var(--border))"
            strokeWidth="0.5"
            strokeDasharray="3 3"
            opacity="0.5"
          />
        ))}
        <path d={paths.newArea} fill={`url(#${gradId})`} />
        <path
          d={paths.newLine}
          fill="none"
          stroke="rgb(var(--danger))"
          strokeWidth="1.5"
          vectorEffect="non-scaling-stroke"
        />
        <path
          d={paths.resolved}
          fill="none"
          stroke="rgb(var(--ok))"
          strokeWidth="1.5"
          vectorEffect="non-scaling-stroke"
        />
        <path
          d={paths.investigations}
          fill="none"
          stroke="rgb(var(--accent))"
          strokeWidth="1.5"
          strokeDasharray="4 3"
          vectorEffect="non-scaling-stroke"
        />
        {hover != null && (
          <line
            x1={(hover / Math.max(1, buckets.length - 1)) * w}
            x2={(hover / Math.max(1, buckets.length - 1)) * w}
            y1="0"
            y2={h}
            stroke="rgb(var(--text-faint))"
            strokeWidth="0.75"
            vectorEffect="non-scaling-stroke"
          />
        )}
      </svg>

      {/* 图例 + 悬停读数。读数放在图例位置而非跟随光标的浮层:
          浮层会挡住曲线本身,而这张图只有 24 个点,固定位置足够。 */}
      <div className="mt-1 flex flex-wrap items-center gap-x-4 gap-y-1 text-2xs">
        <LegendDot className="bg-danger" label="新增" />
        <LegendDot className="bg-ok" label="已解决" />
        <LegendDot className="bg-accent" label="调查" dashed />
        <span className="ml-auto tabular text-faint">
          {hoveredBucket
            ? `${formatClock(hoveredBucket.ts)} · 新增 ${hoveredBucket.new} · 解决 ${hoveredBucket.resolved} · 调查 ${hoveredBucket.investigations}`
            : `峰值 ${max}`}
        </span>
      </div>
    </div>
  )
}

function LegendDot({
  className,
  label,
  dashed,
}: {
  className: string
  label: string
  dashed?: boolean
}) {
  return (
    <span className="inline-flex items-center gap-1.5 text-faint">
      <span
        className={cn(
          'inline-block h-0.5 w-3 rounded-full',
          className,
          dashed && 'opacity-70',
        )}
        aria-hidden
      />
      {label}
    </span>
  )
}

// ── 迷你柱状图 ──────────────────────────────────────────
// 嵌在统计卡里,给一个数字配上"它最近怎么变的"。
export function Sparkbars({
  values,
  className,
  tone = 'bg-accent',
}: {
  values: number[]
  className?: string
  tone?: string
}) {
  const max = Math.max(1, ...values)
  return (
    <div className={cn('flex h-6 items-end gap-px', className)}>
      {values.map((v, i) => (
        <div
          key={i}
          className={cn('flex-1 rounded-sm', tone)}
          style={{
            // 非零值至少给 8% 高度,否则 1 与 0 在图上看起来一样 ——
            // "有一个"和"没有"是完全不同的信息。
            height: `${v > 0 ? Math.max((v / max) * 100, 8) : 2}%`,
            opacity: v > 0 ? 1 : 0.3,
          }}
        />
      ))}
    </div>
  )
}
