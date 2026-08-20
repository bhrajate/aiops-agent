import { useState } from 'react'
import type { SignalRequest, SignalSource, SignalType } from '@/api/types'
import { Button, Field, inputCls, selectCls, Callout } from './ui'
import { useInjectSignal } from '@/hooks/queries'
import { pushToast } from './Toast'
import { HttpError } from '@/api/client'
import { FlaskConical, X } from 'lucide-react'
import { cn } from '@/lib/format'

const SOURCES: SignalSource[] = [
  'alertmanager',
  'kubernetes',
  'cicd',
  'itsm',
  'slo',
]
const TYPES: SignalType[] = ['alert', 'change', 'event', 'resolved']

// 预设几个常见故障形态。手填 6 个字段才能造一条信号太慢,
// 而演示与联调时最常做的就是"再来一条一样的"。
const PRESETS: Array<{ name: string; apply: () => SignalRequest }> = [
  {
    name: '高错误率',
    apply: () => ({
      ...base(),
      labels: { alertname: 'HighErrorRate', rule_id: 'r-http-5xx' },
    }),
  },
  {
    name: 'Pod 崩溃循环',
    apply: () => ({
      ...base(),
      source: 'kubernetes',
      severity: 'critical',
      resource_ref: {
        namespace: 'payment',
        kind: 'Pod',
        name: 'checkout-7d9f8bc4f5-x2k9p',
      },
      labels: { alertname: 'CrashLoopBackOff', rule_id: 'r-k8s-crashloop' },
    }),
  },
  {
    name: '发布变更',
    apply: () => ({
      ...base(),
      source: 'cicd',
      signal_type: 'change',
      severity: 'info',
      labels: {
        alertname: 'Deployment',
        rule_id: 'r-cicd',
        version: 'v2.3.1',
      },
    }),
  },
  {
    name: 'SLO 燃尽',
    apply: () => ({
      ...base(),
      source: 'slo',
      severity: 'warning',
      labels: { alertname: 'ErrorBudgetBurn', rule_id: 'r-slo-burn' },
    }),
  },
]

function base(): SignalRequest {
  return {
    tenant_id: 'default',
    cluster_id: 'prod-cn-1',
    source: 'alertmanager',
    signal_type: 'alert',
    resource_ref: {
      namespace: 'payment',
      kind: 'Deployment',
      name: 'checkout',
    },
    severity: 'critical',
    starts_at: new Date().toISOString(),
    labels: { alertname: 'HighErrorRate', rule_id: 'r-123' },
  }
}

/** 模拟注入 Signal 的演示面板:POST /v1/signals */
export function SignalInjector({ onClose }: { onClose: () => void }) {
  const [form, setForm] = useState<SignalRequest>(base)
  const [hint, setHint] = useState<string | null>(null)
  const inject = useInjectSignal()

  function set<K extends keyof SignalRequest>(k: K, v: SignalRequest[K]) {
    setForm((f) => ({ ...f, [k]: v }))
  }
  function setRef(k: keyof SignalRequest['resource_ref'], v: string) {
    setForm((f) => ({ ...f, resource_ref: { ...f.resource_ref, [k]: v } }))
  }

  async function submit() {
    setHint(null)
    try {
      await inject.mutateAsync({ ...form, starts_at: new Date().toISOString() })
      pushToast('已注入 Signal,聚合完成后会出现在告警列表', 'success')
      onClose()
    } catch (e) {
      if (e instanceof HttpError && e.status === 401) {
        // 401 在这里不是登录失效:/v1/signals 走 webhook HMAC 鉴权。
        // 不能让它触发全局跳登录(endpoints 里已 skipAuthRedirect),
        // 这里给出真正的原因。
        setHint(
          '注入需 webhook 签名。演示环境请把后端 AIOPS_WEBHOOK_SECRET 置空后重试 —— 这个端点用 HMAC 签名鉴权,不认用户登录令牌。',
        )
      } else {
        setHint(e instanceof HttpError ? `失败:${e.message}` : '注入失败')
      }
    }
  }

  return (
    <div
      role="dialog"
      aria-modal
      aria-label="模拟注入 Signal"
      className="anim-fade fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      onClick={onClose}
    >
      <div
        className="anim-scale max-h-[86vh] w-full max-w-lg overflow-auto rounded-xl border border-line bg-card shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="sticky top-0 flex items-center justify-between border-b border-line-soft bg-card px-4 py-3">
          <h3 className="flex items-center gap-2 text-sm font-semibold text-content">
            <FlaskConical className="h-4 w-4 text-accent" />
            模拟注入 Signal
          </h3>
          <button
            onClick={onClose}
            aria-label="关闭"
            className="rounded p-1 text-muted hover:bg-card-soft hover:text-content"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <div className="space-y-3 p-4">
          <div>
            <div className="mb-1.5 text-xs text-muted">快速预设</div>
            <div className="flex flex-wrap gap-1.5">
              {PRESETS.map((p) => (
                <button
                  key={p.name}
                  type="button"
                  onClick={() => setForm(p.apply())}
                  className="rounded-md border border-line-soft bg-bg-soft px-2 py-1 text-2xs text-muted transition-colors hover:border-line hover:text-content"
                >
                  {p.name}
                </button>
              ))}
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <Field label="来源 source">
              <select
                value={form.source}
                onChange={(e) => set('source', e.target.value as SignalSource)}
                className={cn(selectCls, 'w-full')}
              >
                {SOURCES.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="类型 signal_type">
              <select
                value={form.signal_type}
                onChange={(e) =>
                  set('signal_type', e.target.value as SignalType)
                }
                className={cn(selectCls, 'w-full')}
              >
                {TYPES.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </select>
            </Field>

            <Field label="集群 cluster_id">
              <input
                value={form.cluster_id}
                onChange={(e) => set('cluster_id', e.target.value)}
                className={inputCls}
              />
            </Field>
            <Field label="严重级别 severity">
              <input
                value={form.severity}
                onChange={(e) => set('severity', e.target.value)}
                className={inputCls}
              />
            </Field>

            <Field label="命名空间 namespace">
              <input
                value={form.resource_ref.namespace}
                onChange={(e) => setRef('namespace', e.target.value)}
                className={inputCls}
              />
            </Field>
            <Field label="资源类型 kind">
              <input
                value={form.resource_ref.kind}
                onChange={(e) => setRef('kind', e.target.value)}
                className={inputCls}
              />
            </Field>
            <div className="col-span-2">
              <Field
                label="资源名 name"
                hint="Pod 名会被归约到所属工作负载后再算影响面"
              >
                <input
                  value={form.resource_ref.name}
                  onChange={(e) => setRef('name', e.target.value)}
                  className={inputCls}
                />
              </Field>
            </div>
          </div>

          {hint && <Callout tone="warn">{hint}</Callout>}
        </div>

        <div className="flex items-center justify-end gap-2 border-t border-line-soft px-4 py-3">
          <Button variant="secondary" size="sm" onClick={onClose}>
            取消
          </Button>
          <Button
            variant="primary"
            size="sm"
            loading={inject.isPending}
            onClick={submit}
          >
            注入
          </Button>
        </div>
      </div>
    </div>
  )
}
