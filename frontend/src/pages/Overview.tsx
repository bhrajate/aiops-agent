import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  RefreshCw,
  FlaskConical,
  Siren,
  Search,
  Timer,
  CircleDollarSign,
  AlertTriangle,
  Activity,
  Database,
  ShieldCheck,
  ArrowRight,
} from 'lucide-react'
import { useOverview } from '@/hooks/queries'
import { useAuth } from '@/auth/context'
import {
  Card,
  CardHeader,
  PageHeader,
  Button,
  StatCard,
  ErrorState,
  Skeleton,
  SegmentedControl,
  Callout,
} from '@/components/ui'
import { BarDistribution, TrendChart } from '@/components/charts'
import { QueueHealthBadge } from '@/components/Badges'
import { SignalInjector } from '@/components/SignalInjector'
import {
  cn,
  formatCost,
  formatCount,
  formatDuration,
  formatTokens,
  relativeTime,
  faultCategoryLabel,
  feedbackLabel,
  evidenceTypeLabel,
} from '@/lib/format'
import { phaseLabel, statusLabel } from '@/components/Badges'
import { HttpError } from '@/api/client'
import type { InvestigationPhase, IncidentStatus } from '@/api/types'

const WINDOWS = [
  { value: 1, label: '1h' },
  { value: 6, label: '6h' },
  { value: 24, label: '24h' },
  { value: 168, label: '7d' },
  { value: 720, label: '30d' },
]

const SEV_COLOR: Record<string, string> = {
  P1: 'bg-p1',
  P2: 'bg-p2',
  P3: 'bg-p3',
  P4: 'bg-p4',
}

const STATUS_COLOR: Record<string, string> = {
  open: 'bg-danger',
  acknowledged: 'bg-warn',
  resolved: 'bg-ok',
  closed: 'bg-faint',
}

const FEEDBACK_COLOR: Record<string, string> = {
  confirm: 'bg-ok',
  correct: 'bg-warn',
  reject: 'bg-danger',
  close: 'bg-faint',
}

export function OverviewPage() {
  const [hours, setHours] = useState(24)
  const [showInjector, setShowInjector] = useState(false)
  const navigate = useNavigate()
  const { canWrite, canReadAudit } = useAuth()
  const { data: ov, isLoading, error, refetch, isFetching } = useOverview(hours)

  return (
    <>
      <PageHeader
        title="值班总览"
        subtitle={
          ov
            ? `窗口 ${windowLabel(hours)} · 数据更新于 ${relativeTime(ov.generated_at)}`
            : '加载中…'
        }
        actions={
          <>
            <SegmentedControl
              value={hours}
              options={WINDOWS}
              onChange={setHours}
            />
            <Button
              variant="ghost"
              size="sm"
              onClick={() => refetch()}
              aria-label="刷新"
            >
              <RefreshCw
                className={cn('h-4 w-4', isFetching && 'animate-spin')}
              />
            </Button>
            {canWrite && (
              <Button
                variant="primary"
                size="sm"
                onClick={() => setShowInjector(true)}
              >
                <FlaskConical className="h-3.5 w-3.5" />
                注入 Signal
              </Button>
            )}
          </>
        }
      />

      <div className="anim-rise px-6 py-5">
        {error ? (
          <Card>
            <ErrorState
              message={
                error instanceof HttpError
                  ? `${error.message}(${error.code})`
                  : '加载失败,请确认后端 :8088 已启动'
              }
              onRetry={() => refetch()}
            />
          </Card>
        ) : isLoading || !ov ? (
          <OverviewSkeleton />
        ) : (
          <div className="space-y-5">
            {/* 待处置提示条:未认领的 P1 是唯一值得打断视线的事。
                没有时不显示 —— 常驻的"一切正常"横幅会被大脑过滤掉,
                真出事时那块区域也就不再被看见。 */}
            {ov.open_p1 > 0 && (
              <Callout tone="danger" icon={<AlertTriangle className="h-4 w-4" />}>
                <span className="font-medium">
                  {ov.open_p1} 个 P1 未闭环
                </span>
                {ov.unacknowledged > 0 && (
                  <>
                    ,其中 {ov.unacknowledged} 个还没有人认领。
                  </>
                )}
                <button
                  onClick={() => navigate('/incidents?severity=P1&status=open')}
                  className="ml-2 inline-flex items-center gap-1 underline hover:no-underline"
                >
                  立即查看
                  <ArrowRight className="h-3 w-3" />
                </button>
              </Callout>
            )}
            {ov.stalled_investigations > 0 && (
              <Callout tone="warn" icon={<Timer className="h-4 w-4" />}>
                <span className="font-medium">
                  {ov.stalled_investigations} 次调查运行超过 10 分钟仍未结束
                </span>
                {' '}—— 可能是 worker 卡住或工具调用挂起,不会自行恢复。
                <button
                  onClick={() => navigate('/investigations?active=true')}
                  className="ml-2 inline-flex items-center gap-1 underline hover:no-underline"
                >
                  查看调查队列
                  <ArrowRight className="h-3 w-3" />
                </button>
              </Callout>
            )}
            {ov.queue && ov.queue.health !== 'ok' && (
              <Callout tone={ov.queue.health === 'stuck' ? 'danger' : 'warn'}
                icon={<Database className="h-4 w-4" />}>
                <span className="font-medium">
                  投递管道{ov.queue.health === 'stuck' ? '卡住' : '滞后'}
                </span>
                :最老待投递记录已等待{' '}
                {formatDuration(ov.queue.oldest_pending_age_sec)}
                ,待投递 {formatCount(ov.queue.outbox_pending)} 条
                {ov.queue.outbox_dead > 0 &&
                  `,已放弃 ${ov.queue.outbox_dead} 条`}
                {ov.queue.dead_letters > 0 &&
                  `,死信 ${ov.queue.dead_letters} 条`}
                。Signal 仍在接收但可能不再产生 Incident。
              </Callout>
            )}

            {/* 现状指标 */}
            <section>
              <h2 className="mb-2 text-xs font-semibold uppercase tracking-wide text-faint">
                当前状态
              </h2>
              <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
                <StatCard
                  label="未闭环"
                  value={ov.open_total}
                  hint="open + 处置中"
                  icon={<Siren className="h-3.5 w-3.5" />}
                  tone={ov.open_total > 0 ? 'warn' : 'ok'}
                  emphasis={ov.open_total > 0}
                  onClick={() => navigate('/incidents?status=open')}
                />
                <StatCard
                  label="P1"
                  value={ov.open_p1}
                  hint="未闭环的最高级别"
                  tone="danger"
                  emphasis={ov.open_p1 > 0}
                  onClick={() => navigate('/incidents?severity=P1')}
                />
                <StatCard
                  label="P2"
                  value={ov.open_p2}
                  hint="未闭环"
                  tone="warn"
                  emphasis={ov.open_p2 > 0}
                  onClick={() => navigate('/incidents?severity=P2')}
                />
                <StatCard
                  label="未认领"
                  value={ov.unacknowledged}
                  hint="还没有人接手"
                  tone="danger"
                  emphasis={ov.unacknowledged > 0}
                  onClick={() => navigate('/incidents?status=open')}
                />
                <StatCard
                  label="进行中调查"
                  value={ov.active_investigations}
                  hint={
                    ov.stalled_investigations > 0
                      ? `${ov.stalled_investigations} 个疑似卡住`
                      : '非终态'
                  }
                  icon={<Search className="h-3.5 w-3.5" />}
                  tone={ov.stalled_investigations > 0 ? 'warn' : 'accent'}
                  emphasis={ov.active_investigations > 0}
                  onClick={() => navigate('/investigations?active=true')}
                />
                <StatCard
                  label="平均处置时长"
                  // 样本不足时显示破折号而非 0 —— 0 会被读成"秒级解决"。
                  value={
                    ov.mttr_seconds === null
                      ? '—'
                      : formatDuration(ov.mttr_seconds)
                  }
                  hint={
                    ov.mttr_sample_size > 0
                      ? `${ov.mttr_sample_size} 个样本`
                      : '窗口内无已解决样本'
                  }
                  icon={<Timer className="h-3.5 w-3.5" />}
                />
              </div>
            </section>

            {/* 趋势 + 级别分布 */}
            <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
              <Card className="lg:col-span-2">
                <CardHeader
                  icon={<Activity className="h-4 w-4 text-accent" />}
                  title="趋势"
                  subtitle={`${windowLabel(hours)} 均分为 24 个时间桶`}
                />
                <div className="p-4">
                  <TrendChart buckets={ov.trend} />
                </div>
              </Card>

              <Card>
                <CardHeader
                  title="级别分布"
                  subtitle={`窗口内 ${formatCount(ov.incidents_in_window)} 个 Incident`}
                />
                <div className="p-4">
                  <BarDistribution
                    data={ov.by_severity}
                    total={ov.incidents_in_window}
                    colorOf={(k) => SEV_COLOR[k] ?? 'bg-accent'}
                    onSelect={(k) => navigate(`/incidents?severity=${k}`)}
                  />
                  <div className="mt-4 border-t border-line-soft pt-3">
                    <div className="mb-2 text-xs font-medium text-muted">
                      状态分布
                    </div>
                    <BarDistribution
                      data={ov.by_status}
                      total={ov.incidents_in_window}
                      colorOf={(k) => STATUS_COLOR[k] ?? 'bg-accent'}
                      labelOf={(k) => statusLabel(k as IncidentStatus)}
                      onSelect={(k) => navigate(`/incidents?status=${k}`)}
                    />
                  </div>
                </div>
              </Card>
            </div>

            {/* 窗口内量级 */}
            <section>
              <h2 className="mb-2 text-xs font-semibold uppercase tracking-wide text-faint">
                {windowLabel(hours)} 内
              </h2>
              <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
                <StatCard
                  label="新增 Incident"
                  value={formatCount(ov.incidents_in_window)}
                  hint={`聚合 ${formatCount(ov.signals_aggregated)} 条 Signal`}
                />
                <StatCard
                  label="发起调查"
                  value={formatCount(ov.investigations_started)}
                />
                <StatCard
                  label="调查 P95 时长"
                  value={
                    ov.p95_investigation_seconds === null
                      ? '—'
                      : formatDuration(ov.p95_investigation_seconds)
                  }
                  hint={
                    ov.investigation_sample_size > 0
                      ? `${ov.investigation_sample_size} 次已结束`
                      : '窗口内无已结束调查'
                  }
                />
                <StatCard
                  label="模型成本"
                  value={formatCost(ov.cost_usd)}
                  hint={`${formatTokens(ov.tokens)} tokens`}
                  icon={<CircleDollarSign className="h-3.5 w-3.5" />}
                />
                <StatCard
                  label="工具调用"
                  value={formatCount(ov.tool_calls)}
                  hint="全部只读"
                />
                <StatCard
                  label="待审评测用例"
                  value={formatCount(ov.golden_pending)}
                  hint="人工反馈提升而来"
                  icon={<FlaskConical className="h-3.5 w-3.5" />}
                  tone="warn"
                  emphasis={ov.golden_pending > 0}
                  onClick={() => navigate('/golden-cases')}
                />
              </div>
            </section>

            {/* 分布细项 */}
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
              <Card>
                <CardHeader title="调查阶段" />
                <div className="p-4">
                  <BarDistribution
                    data={ov.by_phase}
                    total={ov.investigations_started}
                    labelOf={(k) => phaseLabel(k as InvestigationPhase)}
                    onSelect={(k) => navigate(`/investigations?phase=${k}`)}
                  />
                </div>
              </Card>

              <Card>
                <CardHeader
                  title="诊断结论"
                  subtitle="允许返回“无法确定”"
                />
                <div className="p-4">
                  <BarDistribution
                    data={ov.by_diagnosis}
                    colorOf={(k) =>
                      k === 'resolved'
                        ? 'bg-ok'
                        : k === 'unresolved'
                          ? 'bg-warn'
                          : 'bg-faint'
                    }
                    labelOf={(k) =>
                      k === 'resolved'
                        ? '已定位'
                        : k === 'unresolved'
                          ? '未定位'
                          : '不确定'
                    }
                    emptyHint="窗口内无已完成诊断"
                  />
                </div>
              </Card>

              <Card>
                <CardHeader title="故障类别" />
                <div className="p-4">
                  <BarDistribution
                    data={ov.by_fault_category}
                    total={ov.incidents_in_window}
                    labelOf={faultCategoryLabel}
                  />
                </div>
              </Card>

              <Card>
                <CardHeader
                  title="人工反馈"
                  // 不显示"采纳率"这一个数:它会丢掉分子分母,而"低采纳率"
                  // 与"根本没人给反馈"是完全不同的问题,处置方式相反。
                  subtitle="按动作分维度,不折算成采纳率"
                />
                <div className="p-4">
                  <BarDistribution
                    data={ov.by_feedback}
                    colorOf={(k) => FEEDBACK_COLOR[k] ?? 'bg-accent'}
                    labelOf={feedbackLabel}
                    emptyHint="窗口内无人工反馈"
                  />
                  <div className="mt-4 border-t border-line-soft pt-3">
                    <div className="mb-2 text-xs font-medium text-muted">
                      证据类型
                    </div>
                    <BarDistribution
                      data={ov.by_evidence_type}
                      labelOf={evidenceTypeLabel}
                      emptyHint="窗口内无证据产出"
                    />
                  </div>
                </div>
              </Card>
            </div>

            {/* 管道健康:仅 sre/admin 可见(后端也只对这些角色返回 queue) */}
            {canReadAudit && (
              <Card>
                <CardHeader
                  icon={<ShieldCheck className="h-4 w-4 text-accent" />}
                  title="投递管道"
                  subtitle="按最老待投递记录的年龄判定,不按条数"
                  right={
                    ov.queue ? (
                      <QueueHealthBadge health={ov.queue.health} />
                    ) : (
                      <span className="text-2xs text-faint">不可用</span>
                    )
                  }
                />
                <div className="p-4">
                  {ov.queue === null ? (
                    // 查询失败时明确说"读不到",不显示 0 ——
                    // 0 会被读成"队列是空的",恰好掩盖 outbox 卡死。
                    <p className="text-xs text-faint">
                      队列状态查询失败。这不代表队列是空的 —— 请检查数据库连通性与
                      control-plane 日志。
                    </p>
                  ) : (
                    <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
                      <StatCard
                        label="待投递"
                        value={formatCount(ov.queue.outbox_pending)}
                      />
                      <StatCard
                        label="最老待投递"
                        value={formatDuration(ov.queue.oldest_pending_age_sec)}
                        hint="判定卡住的主指标"
                        tone={
                          ov.queue.oldest_pending_age_sec >= 300
                            ? 'danger'
                            : ov.queue.oldest_pending_age_sec >= 60
                              ? 'warn'
                              : 'ok'
                        }
                      />
                      <StatCard
                        label="已放弃投递"
                        value={formatCount(ov.queue.outbox_dead)}
                        hint="重试耗尽,需人工处理"
                        tone="danger"
                        emphasis={ov.queue.outbox_dead > 0}
                      />
                      <StatCard
                        label="死信"
                        value={formatCount(ov.queue.dead_letters)}
                        tone="warn"
                        emphasis={ov.queue.dead_letters > 0}
                      />
                    </div>
                  )}
                </div>
              </Card>
            )}
          </div>
        )}
      </div>

      {showInjector && (
        <SignalInjector onClose={() => setShowInjector(false)} />
      )}
    </>
  )
}

function windowLabel(hours: number): string {
  if (hours < 24) return `${hours} 小时`
  const d = Math.round(hours / 24)
  return `${d} 天`
}

function OverviewSkeleton() {
  return (
    <div className="space-y-5">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-[86px]" />
        ))}
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Skeleton className="h-56 lg:col-span-2" />
        <Skeleton className="h-56" />
      </div>
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-44" />
        ))}
      </div>
    </div>
  )
}
