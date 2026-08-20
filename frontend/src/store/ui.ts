// 界面壳的本地状态:侧边栏折叠、命令面板开关。
//
// 与 theme.ts 同构的极简 store。折叠状态持久化 —— 值班人员调好的布局
// 不该每次刷新都回到默认。

import { useEffect, useState } from 'react'

const SIDEBAR_KEY = 'aiops_sidebar_collapsed'

function readCollapsed(): boolean {
  try {
    return localStorage.getItem(SIDEBAR_KEY) === '1'
  } catch {
    return false
  }
}

interface UiState {
  sidebarCollapsed: boolean
  paletteOpen: boolean
}

const state: UiState = {
  sidebarCollapsed: readCollapsed(),
  paletteOpen: false,
}

const listeners = new Set<() => void>()

function emit() {
  for (const fn of listeners) fn()
}

export function toggleSidebar(): void {
  state.sidebarCollapsed = !state.sidebarCollapsed
  try {
    localStorage.setItem(SIDEBAR_KEY, state.sidebarCollapsed ? '1' : '0')
  } catch {
    // 忽略
  }
  emit()
}

export function setPaletteOpen(open: boolean): void {
  state.paletteOpen = open
  emit()
}

export function getUiState(): Readonly<UiState> {
  return state
}

export function useUi(): UiState & {
  toggleSidebar: () => void
  setPaletteOpen: (open: boolean) => void
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
    sidebarCollapsed: state.sidebarCollapsed,
    paletteOpen: state.paletteOpen,
    toggleSidebar,
    setPaletteOpen,
  }
}
