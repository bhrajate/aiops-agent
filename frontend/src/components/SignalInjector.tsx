import { useState } from 'react'
import type { SignalRequest, SignalSource, SignalType } from '@/api/types'
import { Button } from './ui'
import { useInjectSignal } from '@/hooks/queries'
import { HttpError } from '@/api/client'
import { FlaskConical, X } from 'lucide-react'

const SOURCES: SignalSource[] = [
  'alertmanager',
  'kubernetes',
  'cicd',
  'itsm',
  'slo',
]
const TYPES: SignalType[] = ['alert', 'change', 'event', 'resolved']

function defaults(): SignalRequest {
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

const inputCls =
  'w-full rounded-md border border-surface-600 bg-surface-900 px-2.5 py-1.5 text-sm text-slate-200 outline-none focus:border-accent'

/** 模拟注入 Signal 的演示面板:POST /v1/signals */
export function SignalInjector({ onClose }: { onClose: () => void }) {
  const [form, setForm] = useState<SignalRequest>(defaults)
  const [msg, setMsg] = useState<string | null>(null)
  const inject = useInjectSignal()

  function set<K extends keyof SignalRequest>(k: K, v: SignalRequest[K]) {
    setForm((f) => ({ ...f, [k]: v }))
  }
  function setRef(k: keyof SignalRequest['resource_ref'], v: string) {
    setForm((f) => ({ ...f, resource_ref: { ...f.resource_ref, [k]: v } }))
  }

  async function submit() {
    setMsg(null)
    try {
      await inject.mutateAsync({ ...form, starts_at: new Date().toISOString() })
      setMsg('已注入 Signal,稍候刷新 Incident 列表')
    } catch (e) {
      if (e instanceof HttpError && e.status === 401) {
        setMsg(
          '注入需 webhook 签名:请将后端 AIOPS_WEBHOOK_SECRET 置空后重试(演示模式放行)。',
        )
      } else {
        setMsg(e instanceof HttpError ? `失败:${e.message}` : '注入失败')
      }
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      onClick={onClose}
    >
      <div
        className="w-full max-w-lg rounded-lg border border-surface-600 bg-surface-850 shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-surface-700 px-4 py-3">
          <h3 className="flex items-center gap-2 text-sm font-semibold text-slate-100">
            <FlaskConical className="h-4 w-4 text-accent" />
            模拟注入 Signal
          </h3>
          <button
            onClick={onClose}
            className="rounded p-1 text-slate-400 hover:bg-surface-700 hover:text-slate-200"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <p className="border-b border-surface-700 bg-amber-500/5 px-4 py-2 text-[11px] leading-relaxed text-amber-300/80">
          演示注入需后端将 AIOPS_WEBHOOK_SECRET 配置为空时可用(webhook HMAC
          签名鉴权,非用户登录令牌)。配置了 secret 时此处会返回 401。
        </p>

        <div className="grid grid-cols-2 gap-3 p-4">
          <label className="col-span-1 text-xs text-slate-400">
            来源 source
            <select
              value={form.source}
              onChange={(e) => set('source', e.target.value as SignalSource)}
              className={inputCls}
            >
              {SOURCES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </label>
          <label className="col-span-1 text-xs text-slate-400">
            类型 signal_type
            <select
              value={form.signal_type}
              onChange={(e) => set('signal_type', e.target.value as SignalType)}
              className={inputCls}
            >
              {TYPES.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </select>
          </label>

          <label className="col-span-1 text-xs text-slate-400">
            集群 cluster_id
            <input
              value={form.cluster_id}
              onChange={(e) => set('cluster_id', e.target.value)}
              className={inputCls}
            />
          </label>
          <label className="col-span-1 text-xs text-slate-400">
            严重级别 severity
            <input
              value={form.severity}
              onChange={(e) => set('severity', e.target.value)}
              className={inputCls}
            />
          </label>

          <label className="col-span-1 text-xs text-slate-400">
            命名空间 namespace
            <input
              value={form.resource_ref.namespace}
              onChange={(e) => setRef('namespace', e.target.value)}
              className={inputCls}
            />
          </label>
          <label className="col-span-1 text-xs text-slate-400">
            资源类型 kind
            <input
              value={form.resource_ref.kind}
              onChange={(e) => setRef('kind', e.target.value)}
              className={inputCls}
            />
          </label>
          <label className="col-span-2 text-xs text-slate-400">
            资源名 name
            <input
              value={form.resource_ref.name}
              onChange={(e) => setRef('name', e.target.value)}
              className={inputCls}
            />
          </label>
        </div>

        <div className="flex items-center justify-between gap-2 border-t border-surface-700 px-4 py-3">
          <span className="text-xs text-slate-400">{msg}</span>
          <Button variant="primary" loading={inject.isPending} onClick={submit}>
            注入
          </Button>
        </div>
      </div>
    </div>
  )
}
