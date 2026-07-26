import { Routes, Route, Link, Navigate, useNavigate } from 'react-router-dom'
import { IncidentListPage } from '@/pages/IncidentList'
import { IncidentDetailPage } from '@/pages/IncidentDetail'
import { InvestigationViewPage } from '@/pages/InvestigationView'
import { LoginPage } from '@/pages/Login'
import { ProtectedRoute } from '@/auth/ProtectedRoute'
import { useAuth } from '@/auth/context'
import { ToastHost } from '@/components/Toast'
import { Siren, LogOut, UserCircle2 } from 'lucide-react'

function TopBar() {
  const { user, isAuthenticated, logout } = useAuth()
  const navigate = useNavigate()

  function handleLogout() {
    logout()
    navigate('/login', { replace: true })
  }

  return (
    <header className="sticky top-0 z-40 border-b border-surface-700 bg-surface-900/90 backdrop-blur">
      <div className="mx-auto flex max-w-7xl items-center justify-between px-6 py-3">
        <Link to="/" className="flex items-center gap-2">
          <Siren className="h-5 w-5 text-accent" />
          <span className="text-sm font-semibold text-slate-100">
            AIOps Incident Workbench
          </span>
          <span className="rounded bg-surface-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-slate-500">
            read-only
          </span>
        </Link>
        {isAuthenticated && user ? (
          <div className="flex items-center gap-3">
            <div className="flex items-center gap-1.5 text-xs text-slate-300">
              <UserCircle2 className="h-4 w-4 text-slate-400" />
              <span className="font-medium text-slate-200">{user.sub}</span>
              {user.roles?.length > 0 && (
                <span className="flex items-center gap-1">
                  {user.roles.map((r) => (
                    <span
                      key={r}
                      className="rounded bg-surface-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-accent"
                    >
                      {r}
                    </span>
                  ))}
                </span>
              )}
            </div>
            <button
              onClick={handleLogout}
              className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-slate-400 hover:bg-surface-700 hover:text-slate-200"
            >
              <LogOut className="h-3.5 w-3.5" />
              登出
            </button>
          </div>
        ) : (
          <span className="text-xs text-slate-500">SRE 值班台</span>
        )}
      </div>
    </header>
  )
}

export default function App() {
  return (
    <div className="min-h-full">
      <TopBar />
      <main>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route
            path="/"
            element={
              <ProtectedRoute>
                <IncidentListPage />
              </ProtectedRoute>
            }
          />
          <Route
            path="/incidents/:incidentId"
            element={
              <ProtectedRoute>
                <IncidentDetailPage />
              </ProtectedRoute>
            }
          />
          <Route
            path="/investigations/:investigationId"
            element={
              <ProtectedRoute>
                <InvestigationViewPage />
              </ProtectedRoute>
            }
          />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
      <ToastHost />
    </div>
  )
}
