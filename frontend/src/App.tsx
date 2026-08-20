import { Routes, Route, Navigate } from 'react-router-dom'
import { Layout } from '@/components/Layout'
import { OverviewPage } from '@/pages/Overview'
import { IncidentListPage } from '@/pages/IncidentList'
import { IncidentDetailPage } from '@/pages/IncidentDetail'
import { InvestigationListPage } from '@/pages/InvestigationList'
import { InvestigationViewPage } from '@/pages/InvestigationView'
import { GoldenCasesPage } from '@/pages/GoldenCases'
import { KnowledgePage } from '@/pages/Knowledge'
import { AuditPage } from '@/pages/Audit'
import { SettingsPage } from '@/pages/Settings'
import { LoginPage } from '@/pages/Login'
import { ProtectedRoute } from '@/auth/ProtectedRoute'
import { ToastHost } from '@/components/Toast'

export default function App() {
  return (
    <>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route
          element={
            <ProtectedRoute>
              <Layout />
            </ProtectedRoute>
          }
        >
          <Route path="/" element={<OverviewPage />} />
          <Route path="/incidents" element={<IncidentListPage />} />
          <Route
            path="/incidents/:incidentId"
            element={<IncidentDetailPage />}
          />
          <Route
            path="/investigations"
            element={<InvestigationListPage />}
          />
          <Route
            path="/investigations/:investigationId"
            element={<InvestigationViewPage />}
          />
          <Route path="/golden-cases" element={<GoldenCasesPage />} />
          <Route path="/knowledge" element={<KnowledgePage />} />
          <Route path="/audit" element={<AuditPage />} />
          <Route path="/settings" element={<SettingsPage />} />
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
      <ToastHost />
    </>
  )
}
