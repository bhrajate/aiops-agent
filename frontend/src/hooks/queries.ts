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
  type IncidentListParams,
} from '@/api/endpoints'
import type { FeedbackRequest, SignalRequest } from '@/api/types'

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

export function useStartInvestigation() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (incidentId: string) => startInvestigation(incidentId),
    onSuccess: (_data, incidentId) => {
      qc.invalidateQueries({ queryKey: ['incident', incidentId] })
      qc.invalidateQueries({ queryKey: ['incidents'] })
    },
  })
}

export function useCancelInvestigation(investigationId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => cancelInvestigation(investigationId),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ['investigation', investigationId] }),
  })
}

export function useSendFeedback(investigationId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (payload: FeedbackRequest) =>
      sendFeedback(investigationId, payload),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ['investigation', investigationId] }),
  })
}

export function useInjectSignal() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (payload: SignalRequest) => postSignal(payload),
    onSuccess: () => {
      // Signal → Incident 有异步延迟,稍后失效列表
      setTimeout(() => qc.invalidateQueries({ queryKey: ['incidents'] }), 1500)
    },
  })
}
