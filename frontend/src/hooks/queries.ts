// React Query hooks:处理轮询/缓存/失效。
import {
  useQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'
import {
  listIncidents,
  getIncident,
  getInvestigation,
  startInvestigation,
  cancelInvestigation,
  sendFeedback,
  getEvidence,
  postSignal,
  getOverview,
  listInvestigations,
  updateIncidentStatus,
  listAudit,
  listGoldenCases,
  reviewGoldenCase,
  searchKnowledge,
  type IncidentListParams,
  type InvestigationListParams,
  type AuditParams,
} from '@/api/endpoints'
import type {
  FeedbackRequest,
  SignalRequest,
  ReviewStatus,
} from '@/api/types'

export function useIncidents(params: IncidentListParams) {
  return useQuery({
    queryKey: ['incidents', params],
    queryFn: () => listIncidents(params),
    refetchInterval: 10_000,
  })
}

export function useIncident(id: string | undefined) {
  return useQuery({
    queryKey: ['incident', id],
    queryFn: () => getIncident(id!),
    enabled: !!id,
    refetchInterval: 15_000,
  })
}

export function useInvestigation(
  id: string | undefined,
  live: boolean,
) {
  return useQuery({
    queryKey: ['investigation', id],
    queryFn: () => getInvestigation(id!),
    enabled: !!id,
    // 调查进行中时高频轮询兜底(SSE 之外),结束后放缓
    refetchInterval: live ? 5_000 : 20_000,
  })
}

export function useEvidence(id: string | undefined) {
  return useQuery({
    queryKey: ['evidence', id],
    queryFn: () => getEvidence(id!),
    enabled: !!id,
    staleTime: 60_000,
  })
}

// 值班总览。10s 轮询:比 incident 列表更快,因为它是首屏的"现在怎么样"。
export function useOverview(hours: number) {
  return useQuery({
    queryKey: ['overview', hours],
    queryFn: () => getOverview(hours),
    refetchInterval: 10_000,
    // 窗口切换时保留上一份数据,避免整屏塌成骨架屏 ——
    // 值班台上"数字突然消失"比"数字旧了一秒"更让人紧张。
    placeholderData: (prev) => prev,
  })
}

export function useInvestigationList(params: InvestigationListParams) {
  return useQuery({
    queryKey: ['investigations', params],
    queryFn: () => listInvestigations(params),
    refetchInterval: 10_000,
    placeholderData: (prev) => prev,
  })
}

export function useAudit(params: AuditParams) {
  return useQuery({
    queryKey: ['audit', params],
    queryFn: () => listAudit(params),
    // 审计是事后追溯,不需要秒级刷新;30s 足够且省一次全表扫描。
    refetchInterval: 30_000,
    placeholderData: (prev) => prev,
  })
}

export function useGoldenCases(status: ReviewStatus | 'all') {
  return useQuery({
    queryKey: ['golden-cases', status],
    queryFn: () => listGoldenCases(status),
    refetchInterval: 30_000,
    placeholderData: (prev) => prev,
  })
}

// 知识检索:显式触发(有 q 才查),不做轮询。
export function useKnowledgeSearch(q: string, enabled: boolean) {
  return useQuery({
    queryKey: ['knowledge', q],
    queryFn: () => searchKnowledge(q),
    enabled: enabled && q.trim().length > 0,
    staleTime: 60_000,
  })
}

export function useStartInvestigation() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (incidentId: string) => startInvestigation(incidentId),
    onSuccess: (_data, incidentId) => {
      qc.invalidateQueries({ queryKey: ['incident', incidentId] })
      qc.invalidateQueries({ queryKey: ['incidents'] })
      qc.invalidateQueries({ queryKey: ['investigations'] })
      qc.invalidateQueries({ queryKey: ['overview'] })
    },
  })
}

export function useCancelInvestigation(investigationId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => cancelInvestigation(investigationId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['investigation', investigationId] })
      qc.invalidateQueries({ queryKey: ['investigations'] })
      qc.invalidateQueries({ queryKey: ['overview'] })
    },
  })
}

export function useSendFeedback(investigationId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (payload: FeedbackRequest) =>
      sendFeedback(investigationId, payload),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['investigation', investigationId] })
      qc.invalidateQueries({ queryKey: ['incidents'] })
      qc.invalidateQueries({ queryKey: ['overview'] })
      // confirm/correct 会把调查提升为待审评测用例,待审队列要跟着变
      qc.invalidateQueries({ queryKey: ['golden-cases'] })
    },
  })
}

// 认领 / 标记已解决。
export function useUpdateIncidentStatus() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (vars: {
      incidentId: string
      status: 'acknowledged' | 'resolved'
    }) => updateIncidentStatus(vars.incidentId, vars.status),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: ['incident', vars.incidentId] })
      qc.invalidateQueries({ queryKey: ['incidents'] })
      qc.invalidateQueries({ queryKey: ['overview'] })
    },
  })
}

export function useReviewGoldenCase() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (vars: {
      caseId: string
      status: 'approved' | 'rejected'
      note?: string
    }) => reviewGoldenCase(vars.caseId, vars.status, vars.note),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['golden-cases'] })
      qc.invalidateQueries({ queryKey: ['overview'] })
    },
  })
}

export function useInjectSignal() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (payload: SignalRequest) => postSignal(payload),
    onSuccess: () => {
      // Signal → Incident 有异步延迟,稍后失效列表
      setTimeout(() => {
        qc.invalidateQueries({ queryKey: ['incidents'] })
        qc.invalidateQueries({ queryKey: ['overview'] })
      }, 1500)
    },
  })
}
