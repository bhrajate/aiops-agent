{{/* 通用命名与标签 helpers */}}

{{- define "aiops.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "aiops.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* 通用标签 */}}
{{- define "aiops.labels" -}}
app.kubernetes.io/part-of: aiops
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{/* 组件镜像:优先组件级 repository/tag,否则回落到 global */}}
{{- define "aiops.image" -}}
{{- $comp := index . 0 -}}
{{- $root := index . 1 -}}
{{- $repo := $comp.image.repository | default (printf "%s/%s" $root.Values.global.imageRegistry (index . 2)) -}}
{{- $tag := $comp.image.tag | default $root.Values.global.imageTag -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}

{{/* Secret 名称:externalSecret 时用 existingSecret,否则 chart 生成的 aiops-secrets */}}
{{- define "aiops.secretName" -}}
{{- if .Values.secrets.existingSecret -}}
{{- .Values.secrets.existingSecret -}}
{{- else -}}
aiops-secrets
{{- end -}}
{{- end -}}

{{/* cluster-agent URL:mTLS 开则 https,否则 http */}}
{{- define "aiops.clusterAgentUrl" -}}
{{- if .Values.config.clusterAgentUrlOverride -}}
{{- .Values.config.clusterAgentUrlOverride -}}
{{- else if .Values.mtls.enabled -}}
https://cluster-agent.{{ .Release.Namespace }}.svc.cluster.local:9100
{{- else -}}
http://cluster-agent.{{ .Release.Namespace }}.svc.cluster.local:9100
{{- end -}}
{{- end -}}

{{/* control-plane 内部 API URL */}}
{{- define "aiops.controlInternalUrl" -}}
{{- if .Values.config.controlInternalUrlOverride -}}
{{- .Values.config.controlInternalUrlOverride -}}
{{- else -}}
http://control-plane-internal.{{ .Release.Namespace }}.svc.cluster.local:8090
{{- end -}}
{{- end -}}

{{/* 共用 podAntiAffinity 片段(传入 name) */}}
{{- define "aiops.antiAffinity" -}}
{{- $name := index . 0 -}}
{{- $root := index . 1 -}}
{{- if $root.Values.podAntiAffinity }}
affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          topologyKey: kubernetes.io/hostname
          labelSelector:
            matchLabels:
              app.kubernetes.io/name: {{ $name }}
{{- end }}
{{- if $root.Values.topologySpread }}
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: ScheduleAnyway
    labelSelector:
      matchLabels:
        app.kubernetes.io/name: {{ $name }}
{{- end }}
{{- end -}}
