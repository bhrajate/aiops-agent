// 主题偏好:跟随系统 / 深色 / 浅色。
//
// 不用 CSS media query 直接决定,因为值班台需要"我就要深色"这个显式选择 ——
// 白天开着浅色系统但盯着深色面板是常见诉求。三态里 system 只是其中一个。
//
// 用轻量自研 store 而非引入 zustand:整个应用只有主题和侧边栏折叠两处需要
// 跨组件共享的非服务端状态,为此加一个依赖不值得。

import { useEffect, useState } from 'react'

export type ThemePreference = 'system' | 'dark' | 'light'
export type ThemeMode = 'dark' | 'light'

const STORAGE_KEY = 'aiops_theme'

function readPreference(): ThemePreference {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw === 'dark' || raw === 'light' || raw === 'system') return raw
  } catch {
    // localStorage 不可用(隐私模式)——退回默认
  }
  return 'system'
}

function systemMode(): ThemeMode {
  if (typeof window === 'undefined' || !window.matchMedia) return 'dark'
  return window.matchMedia('(prefers-color-scheme: light)').matches
    ? 'light'
    : 'dark'
}

export function resolveMode(pref: ThemePreference): ThemeMode {
  return pref === 'system' ? systemMode() : pref
}

// 把解析后的模式写到 <html> 的 class 上。
// 在模块加载时就调用一次(见 main.tsx 之前的 applyTheme),避免首帧闪白:
// React 挂载前 <html> 上就该有正确的类。
export function applyMode(mode: ThemeMode): void {
  const root = document.documentElement
  root.classList.remove('dark', 'light')
  root.classList.add(mode)
}

const listeners = new Set<() => void>()
let preference: ThemePreference = readPreference()

function emit() {
  for (const fn of listeners) fn()
}

export function setPreference(pref: ThemePreference): void {
  preference = pref
  try {
    localStorage.setItem(STORAGE_KEY, pref)
  } catch {
    // 忽略:内存态仍生效,只是不持久化
  }
  applyMode(resolveMode(pref))
  emit()
}

export function getPreference(): ThemePreference {
  return preference
}

// 初始化:模块加载即应用,并订阅系统主题变化。
// 仅在 preference==='system' 时跟随系统变化 —— 用户显式选了深色,
// 系统切浅色不该把他的选择改掉。
export function initTheme(): void {
  applyMode(resolveMode(preference))
  if (typeof window === 'undefined' || !window.matchMedia) return
  const mq = window.matchMedia('(prefers-color-scheme: light)')
  const onChange = () => {
    if (preference === 'system') {
      applyMode(resolveMode(preference))
      emit()
    }
  }
  // Safari <14 只有 addListener;两个都试。
  if (typeof mq.addEventListener === 'function') {
    mq.addEventListener('change', onChange)
  } else if (typeof mq.addListener === 'function') {
    mq.addListener(onChange)
  }
}

export function useTheme(): {
  preference: ThemePreference
  mode: ThemeMode
  setPreference: (p: ThemePreference) => void
  cycle: () => void
} {
  const [, force] = useState(0)
  useEffect(() => {
    const fn = () => force((n) => n + 1)
    listeners.add(fn)
    return () => {
      listeners.delete(fn)
    }
  }, [])
  return {
    preference,
    mode: resolveMode(preference),
    setPreference,
    // 循环顺序 system → dark → light:从"不确定"走向"明确",
    // 且深色在前(值班台的默认场景)。
    cycle: () => {
      const order: ThemePreference[] = ['system', 'dark', 'light']
      const idx = order.indexOf(preference)
      setPreference(order[(idx + 1) % order.length])
    },
  }
}
