import { useEffect } from 'react'
import { Outlet } from 'react-router-dom'
import { Sidebar } from './Sidebar'
import { CommandPalette } from './CommandPalette'
import { useUi, getUiState } from '@/store/ui'

// 应用外壳:固定高度、侧边栏 + 主区各自滚动。
//
// 用 h-screen + overflow-hidden 而非让 body 滚动:值班台的表格很长,
// 页头需要粘在主区顶部而不是随整页滚走 —— 滚到第 40 行时看不到列名,
// 那张表就没法读了。
export function Layout() {
  const { paletteOpen, setPaletteOpen } = useUi()

  // 全局快捷键:⌘K / Ctrl+K 开命令面板。
  // 绑在 window 上,输入框没有焦点时也能触发。
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (!(e.ctrlKey || e.metaKey)) return
      if (e.key === 'k' || e.key === 'K') {
        e.preventDefault()
        setPaletteOpen(!getUiState().paletteOpen)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [setPaletteOpen])

  return (
    <div className="flex h-screen min-h-0 w-screen min-w-0 overflow-hidden bg-bg text-content">
      <Sidebar />
      <main className="flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto">
        <Outlet />
      </main>
      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
      />
    </div>
  )
}
