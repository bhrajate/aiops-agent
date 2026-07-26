import { Routes, Route, Link, Navigate } from 'react-router-dom'
import { IncidentListPage } from '@/pages/IncidentList'
import { IncidentDetailPage } from '@/pages/IncidentDetail'
import { InvestigationViewPage } from '@/pages/InvestigationView'
import { Siren } from 'lucide-react'

function TopBar() {
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
        <span className="text-xs text-slate-500">SRE 值班台</span>
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
          <Route path="/" element={<IncidentListPage />} />
          <Route path="/incidents/:incidentId" element={<IncidentDetailPage />} />
          <Route
            path="/investigations/:investigationId"
            element={<InvestigationViewPage />}
          />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
    </div>
  )
}
