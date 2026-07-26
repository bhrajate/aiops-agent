import { Component } from 'react'
import type { ErrorInfo, ReactNode } from 'react'
import { AlertTriangle, RotateCcw } from 'lucide-react'

interface Props {
  children: ReactNode
}

interface State {
  error: Error | null
}

/**
 * 根级错误边界:兜底任一组件渲染期抛出的错误,避免整页白屏。
 * 展示友好错误页 + 重载按钮。React 错误边界只能用 class 组件实现。
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // 记录到控制台便于排查;生产可在此接入前端错误上报。
    console.error('[ErrorBoundary] 渲染异常:', error, info.componentStack)
  }

  private handleReload = () => {
    window.location.reload()
  }

  render() {
    if (this.state.error) {
      return (
        <div className="flex min-h-screen items-center justify-center px-4">
          <div className="w-full max-w-md text-center">
            <div className="mb-4 flex justify-center">
              <span className="flex h-12 w-12 items-center justify-center rounded-full bg-red-500/10">
                <AlertTriangle className="h-6 w-6 text-red-400" />
              </span>
            </div>
            <h1 className="mb-2 text-lg font-semibold text-slate-100">
              页面出错了
            </h1>
            <p className="mb-1 text-sm text-slate-400">
              界面遇到未预期的错误,已停止渲染以避免更多问题。
            </p>
            <p className="mb-5 break-words text-xs text-slate-600">
              {this.state.error.message}
            </p>
            <button
              onClick={this.handleReload}
              className="inline-flex items-center justify-center gap-1.5 rounded-md bg-accent-muted px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent"
            >
              <RotateCcw className="h-4 w-4" />
              重新加载
            </button>
          </div>
        </div>
      )
    }
    return this.props.children
  }
}
