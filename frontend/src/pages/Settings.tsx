import {
  Settings as SettingsIcon,
  Sun,
  Moon,
  Monitor,
  Lock,
  ShieldCheck,
  KeyRound,
  Server,
} from 'lucide-react'
import { useAuth } from '@/auth/context'
import { useTheme } from '@/store/theme'
import type { ThemePreference } from '@/store/theme'
import {
  Card,
  CardHeader,
  PageHeader,
  InfoItem,
  Mono,
  Callout,
  SegmentedControl,
} from '@/components/ui'
import { RoleBadge } from '@/components/Badges'

// 设置页。刻意保持窄:这个系统的配置在部署侧(环境变量 / Helm values),
// 不在界面里。把只读的运行信息集中展示,比给一堆改不了的开关更有用。
export function SettingsPage() {
  const { user, canWrite, canReadAudit, canReviewGolden } = useAuth()
  const { preference, setPreference } = useTheme()

  const themeOpts: Array<{
    value: ThemePreference
    label: React.ReactNode
  }> = [
    {
      value: 'system',
      label: (
        <span className="inline-flex items-center gap-1">
          <Monitor className="h-3 w-3" />
          跟随系统
        </span>
      ),
    },
    {
      value: 'dark',
      label: (
        <span className="inline-flex items-center gap-1">
          <Moon className="h-3 w-3" />
          深色
        </span>
      ),
    },
    {
      value: 'light',
      label: (
        <span className="inline-flex items-center gap-1">
          <Sun className="h-3 w-3" />
          浅色
        </span>
      ),
    },
  ]

  const apiBase = import.meta.env.VITE_API_BASE || '(同源 /v1,经反向代理)'

  return (
    <>
      <PageHeader
        title="设置"
        subtitle="系统配置在部署侧;这里展示当前会话与运行时信息"
      />

      <div className="anim-rise mx-auto max-w-3xl space-y-4 px-6 py-5">
        <Card>
          <CardHeader
            icon={<SettingsIcon className="h-4 w-4 text-accent" />}
            title="外观"
          />
          <div className="space-y-3 p-4">
            <div className="flex items-center justify-between gap-3">
              <div>
                <div className="text-xs text-content">主题</div>
                <div className="mt-0.5 text-2xs text-faint">
                  选择后会记住;跟随系统时随操作系统切换
                </div>
              </div>
              <SegmentedControl
                value={preference}
                options={themeOpts}
                onChange={setPreference}
              />
            </div>
          </div>
        </Card>

        <Card>
          <CardHeader
            icon={<KeyRound className="h-4 w-4 text-accent" />}
            title="当前会话"
            subtitle="有效权限 = 用户 ∩ Agent 服务身份 ∩ Incident 范围"
          />
          <div className="space-y-3 p-4">
            <div className="grid grid-cols-2 gap-2 md:grid-cols-3">
              <InfoItem label="用户" value={user?.sub ?? '—'} />
              <InfoItem label="邮箱" value={user?.email || '—'} />
              <InfoItem
                label="角色"
                value={
                  <span className="flex flex-wrap gap-1">
                    {user?.roles?.length ? (
                      user.roles.map((r) => <RoleBadge key={r} role={r} />)
                    ) : (
                      '—'
                    )}
                  </span>
                }
              />
            </div>
            {/* ABAC 范围要显式展示:值班人员必须知道自己看到的是全量还是子集,
                否则"列表里没有"会被误读成"没有故障"。 */}
            <div className="grid grid-cols-1 gap-2 md:grid-cols-2">
              <InfoItem
                label="可见集群"
                value={
                  <span className="flex flex-wrap gap-1">
                    {(user?.clusters ?? []).length ? (
                      user!.clusters!.map((c) => (
                        <Mono key={c}>{c === '*' ? '全部' : c}</Mono>
                      ))
                    ) : (
                      '—'
                    )}
                  </span>
                }
              />
              <InfoItem
                label="可见命名空间"
                value={
                  <span className="flex flex-wrap gap-1">
                    {(user?.namespaces ?? []).length ? (
                      user!.namespaces!.map((n) => (
                        <Mono key={n}>{n === '*' ? '全部' : n}</Mono>
                      ))
                    ) : (
                      '—'
                    )}
                  </span>
                }
              />
            </div>

            <div className="rounded-lg border border-line-soft bg-bg-soft p-3">
              <div className="mb-2 text-2xs text-faint">你的操作权限</div>
              <ul className="space-y-1 text-2xs">
                <PermRow label="查看 Incident 与证据" granted />
                <PermRow label="发起 / 取消调查、提交反馈" granted={canWrite} />
                <PermRow label="审核评测用例" granted={canReviewGolden} />
                <PermRow label="查看审计日志" granted={canReadAudit} />
              </ul>
              <p className="mt-2 text-2xs text-faint">
                界面按角色隐藏无权入口只是体验优化,后端对每个请求独立强制。
              </p>
            </div>
          </div>
        </Card>

        <Card>
          <CardHeader
            icon={<Server className="h-4 w-4 text-accent" />}
            title="运行时"
          />
          <div className="grid grid-cols-1 gap-2 p-4 md:grid-cols-2">
            <InfoItem
              label="API 基地址"
              value={<span className="font-mono text-2xs">{apiBase}</span>}
            />
            <InfoItem
              label="构建模式"
              value={import.meta.env.DEV ? '开发' : '生产'}
            />
          </div>
        </Card>

        <Card>
          <CardHeader
            icon={<Lock className="h-4 w-4 text-accent" />}
            title="安全边界"
            subtitle="这些约束由确定性代码强制,不交给模型判断"
          />
          <div className="space-y-2 p-4">
            <Callout tone="ok" icon={<ShieldCheck className="h-3.5 w-3.5" />}>
              <span className="font-medium">默认只读</span>
              :所有生产工具均为只读(K8s 仅 Get/List),模型没有任何生产写权限,
              <Mono className="mx-1">remediation_proposal</Mono>
              恒为 null。
            </Callout>
            <Callout tone="info">
              <span className="font-medium">确定性护栏</span>
              :权限、预算、限流、调查的触发与停止条件全部由代码执行。
              模型只负责在给定证据下推理,不决定自己能做什么。
            </Callout>
            <Callout tone="info">
              <span className="font-medium">证据优先</span>
              :任何关键结论都必须引用可追溯的 Evidence ID,系统允许返回
              "无法确定" —— 那比编一个看起来合理的根因更有价值。
            </Callout>
          </div>
        </Card>
      </div>
    </>
  )
}

function PermRow({ label, granted }: { label: string; granted?: boolean }) {
  return (
    <li className="flex items-center gap-2">
      <span
        className={
          granted
            ? 'inline-block h-1.5 w-1.5 rounded-full bg-ok'
            : 'inline-block h-1.5 w-1.5 rounded-full bg-faint'
        }
        aria-hidden
      />
      <span className={granted ? 'text-muted' : 'text-faint line-through'}>
        {label}
      </span>
    </li>
  )
}
