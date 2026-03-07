{{/*
Gasboat chart helpers.
Deploys beads daemon, agent controller, coopmux, and optionally PostgreSQL and NATS.
*/}}

{{- define "gasboat.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "gasboat.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{ include "gasboat.selectorLabels" . }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "gasboat.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* ===== Image tag/pull policy helpers ===== */}}

{{/*
Resolve image tag for gasboat-owned images.
When global.latestMode is true, returns "latest" regardless of per-component tag.
Otherwise returns the per-component tag, falling back to Chart.AppVersion.
Usage: include "gasboat.imageTag" (dict "tag" .Values.agents.image.tag "global" .Values.global "Chart" .Chart)
*/}}
{{- define "gasboat.imageTag" -}}
{{- if .global.latestMode -}}
latest
{{- else -}}
{{- .tag | default .Chart.AppVersion -}}
{{- end -}}
{{- end }}

{{/*
Resolve image pull policy for gasboat-owned images.
When global.latestMode is true, always returns "Always".
Otherwise returns the per-component pullPolicy, falling back to "IfNotPresent".
Usage: include "gasboat.imagePullPolicy" (dict "pullPolicy" .Values.agents.image.pullPolicy "global" .Values.global)
*/}}
{{- define "gasboat.imagePullPolicy" -}}
{{- if .global.latestMode -}}
Always
{{- else -}}
{{- .pullPolicy | default "IfNotPresent" -}}
{{- end -}}
{{- end }}

{{/* ===== Beads daemon connection helpers ===== */}}

{{/*
Beads daemon service host.
*/}}
{{- define "gasboat.beads.host" -}}
{{- include "gasboat.beads.fullname" . -}}
{{- end }}

{{/*
Beads daemon gRPC port.
*/}}
{{- define "gasboat.beads.port" -}}
{{- .Values.beads.service.grpcPort | default 9090 -}}
{{- end }}

{{/*
Beads daemon HTTP port.
*/}}
{{- define "gasboat.beads.httpPort" -}}
{{- .Values.beads.service.httpPort | default 8080 -}}
{{- end }}

{{/* ===== Agent Controller component helpers ===== */}}

{{/*
Agent Controller fully qualified name
*/}}
{{- define "gasboat.agents.fullname" -}}
{{- printf "%s-agents" (include "gasboat.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Agent Controller labels
*/}}
{{- define "gasboat.agents.labels" -}}
{{ include "gasboat.labels" . }}
app.kubernetes.io/component: agents
{{- end }}

{{/*
Agent Controller selector labels
*/}}
{{- define "gasboat.agents.selectorLabels" -}}
{{ include "gasboat.selectorLabels" . }}
app.kubernetes.io/component: agents
{{- end }}

{{/*
Agent Controller service account name
*/}}
{{- define "gasboat.agents.serviceAccountName" -}}
{{- if .Values.agents.serviceAccount.create }}
{{- default (printf "%s-sa" (include "gasboat.agents.fullname" .)) .Values.agents.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.agents.serviceAccount.name }}
{{- end }}
{{- end }}

{{/* ===== Agent Pod helpers (SA/RBAC for spawned agent pods) ===== */}}

{{/*
Agent Pod service account name — used by spawned agent pods (not the controller itself).
Defaults to "<release>-agent" when agents.agentServiceAccount.create is true.
*/}}
{{- define "gasboat.coop.serviceAccountName" -}}
{{- if .Values.agents.agentServiceAccount.create }}
{{- default (printf "%s-agent" (include "gasboat.fullname" .)) .Values.agents.agentServiceAccount.name }}
{{- else }}
{{- "default" }}
{{- end }}
{{- end }}

{{/*
Agent Pod cluster-admin service account name — elevated SA for projects that need
full cluster access. Defaults to "<release>-agent-cluster-admin".
*/}}
{{- define "gasboat.coop.clusterAdminServiceAccountName" -}}
{{- default (printf "%s-agent-cluster-admin" (include "gasboat.fullname" .)) .Values.agents.clusterAdmin.name }}
{{- end }}

{{/*
Agent Pod labels
*/}}
{{- define "gasboat.coop.labels" -}}
{{ include "gasboat.labels" . }}
app.kubernetes.io/component: agent
{{- end }}

{{/* ===== Coopmux component helpers ===== */}}

{{/*
Coopmux fully qualified name
*/}}
{{- define "gasboat.coopmux.fullname" -}}
{{- printf "%s-coopmux" (include "gasboat.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Coopmux labels
*/}}
{{- define "gasboat.coopmux.labels" -}}
{{ include "gasboat.labels" . }}
app.kubernetes.io/component: coopmux
{{- end }}

{{/*
Coopmux selector labels
*/}}
{{- define "gasboat.coopmux.selectorLabels" -}}
{{ include "gasboat.selectorLabels" . }}
app.kubernetes.io/component: coopmux
{{- end }}

{{/*
Coopmux auth token secret name
*/}}
{{- define "gasboat.coopmux.authTokenSecretName" -}}
{{- if .Values.coopmux.coopmuxTokenSecret }}
{{- .Values.coopmux.coopmuxTokenSecret }}
{{- else }}
{{- include "gasboat.beads.tokenSecretName" . }}
{{- end }}
{{- end }}

{{/*
Coopmux credential secret name
*/}}
{{- define "gasboat.coopmux.credentialSecretName" -}}
{{- printf "%s-credentials" (include "gasboat.coopmux.fullname" .) }}
{{- end }}

{{/*
Coopmux NATS URL — falls back to in-chart NATS when nats.enabled.
*/}}
{{- define "gasboat.coopmux.natsURL" -}}
{{- if .Values.coopmux.natsURL -}}
{{- .Values.coopmux.natsURL -}}
{{- else -}}
{{- include "gasboat.natsURL" . -}}
{{- end -}}
{{- end }}

{{/*
Coopmux service URL
*/}}
{{- define "gasboat.coopmux.serviceURL" -}}
{{- printf "http://%s:%d" (include "gasboat.coopmux.fullname" .) (int .Values.coopmux.service.port) }}
{{- end }}

{{/*
Slack bridge service URL (auto-wired when slackBridge is enabled)
*/}}
{{- define "gasboat.slackBridge.serviceURL" -}}
{{- printf "http://%s-slack-bridge:%d" (include "gasboat.fullname" .) (int (.Values.slackBridge.service.port | default 8090)) }}
{{- end }}

{{/*
Coopmux ingress middlewares — shared middleware list for all ingress routes.
Accepts a dict with "fullname" and "Values" keys plus an "includeBasicAuth" boolean.
*/}}
{{- define "gasboat.coopmux.ingressMiddlewares" -}}
{{- if .Values.coopmux.ingress.ipWhitelist.enabled }}
- name: {{ .fullname }}-ipwhitelist
{{- end }}
{{- if .Values.coopmux.ingress.rateLimit.enabled }}
- name: {{ .fullname }}-ratelimit
{{- end }}
{{- if and .includeBasicAuth .Values.coopmux.ingress.basicAuth.enabled }}
- name: {{ .fullname }}-basicauth
{{- end }}
{{- if .Values.coopmux.ingress.bearerToken }}
- name: {{ .fullname }}-inject-bearer
{{- end }}
{{- end }}

{{/*
Resolve basic auth secret — returns per-service override if set, otherwise global.
Usage: include "gasboat.basicAuth.secret" (dict "local" .Values.coopmux.ingress.basicAuth.secret "global" .Values.global.basicAuth.secret)
*/}}
{{- define "gasboat.basicAuth.secret" -}}
{{- if .local -}}
{{- .local -}}
{{- else -}}
{{- .global -}}
{{- end -}}
{{- end }}

{{/* ===== Beads3D component helpers ===== */}}

{{/*
Beads3D fully qualified name
*/}}
{{- define "gasboat.beads3d.fullname" -}}
{{- printf "%s-beads3d" (include "gasboat.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Beads3D labels
*/}}
{{- define "gasboat.beads3d.labels" -}}
{{ include "gasboat.labels" . }}
app.kubernetes.io/component: beads3d
{{- end }}

{{/*
Beads3D selector labels
*/}}
{{- define "gasboat.beads3d.selectorLabels" -}}
{{ include "gasboat.selectorLabels" . }}
app.kubernetes.io/component: beads3d
{{- end }}

{{/*
Beads3D daemon URL — falls back to in-chart beads daemon HTTP endpoint.
*/}}
{{- define "gasboat.beads3d.daemonURL" -}}
{{- if .Values.beads3d.daemonURL -}}
{{- .Values.beads3d.daemonURL -}}
{{- else -}}
{{- printf "http://%s:%s" (include "gasboat.beads.fullname" .) (include "gasboat.beads.httpPort" .) -}}
{{- end -}}
{{- end }}

{{/* ===== PostgreSQL component helpers ===== */}}

{{/*
PostgreSQL fully qualified name
*/}}
{{- define "gasboat.postgres.fullname" -}}
{{- printf "%s-postgres" (include "gasboat.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
PostgreSQL labels
*/}}
{{- define "gasboat.postgres.labels" -}}
{{ include "gasboat.labels" . }}
app.kubernetes.io/component: postgres
{{- end }}

{{/*
PostgreSQL selector labels
*/}}
{{- define "gasboat.postgres.selectorLabels" -}}
{{ include "gasboat.selectorLabels" . }}
app.kubernetes.io/component: postgres
{{- end }}

{{/*
PostgreSQL password secret name
*/}}
{{- define "gasboat.postgres.passwordSecretName" -}}
{{- printf "%s-postgres-password" (include "gasboat.fullname" .) }}
{{- end }}

{{/* ===== Beads component helpers ===== */}}

{{/*
Beads fully qualified name
*/}}
{{- define "gasboat.beads.fullname" -}}
{{- printf "%s-beads" (include "gasboat.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Beads labels
*/}}
{{- define "gasboat.beads.labels" -}}
{{ include "gasboat.labels" . }}
app.kubernetes.io/component: beads
{{- end }}

{{/*
Beads selector labels
*/}}
{{- define "gasboat.beads.selectorLabels" -}}
{{ include "gasboat.selectorLabels" . }}
app.kubernetes.io/component: beads
{{- end }}

{{/*
Beads database URL — constructs postgres connection string when postgres.enabled.
Uses K8s variable expansion $(BEADS_DATABASE_PASSWORD) so the password comes from the secret env var.
*/}}
{{- define "gasboat.beads.databaseURL" -}}
{{- if .Values.beads.databaseURL -}}
{{- .Values.beads.databaseURL -}}
{{- else if .Values.postgres.enabled -}}
{{- printf "postgres://postgres:$(BEADS_DATABASE_PASSWORD)@%s:5432/%s?sslmode=disable" (include "gasboat.postgres.fullname" .) (.Values.postgres.database | default "beads") -}}
{{- end -}}
{{- end }}

{{/*
Beads password secret name (for postgres password — same secret as postgres)
*/}}
{{- define "gasboat.beads.passwordSecretName" -}}
{{- include "gasboat.postgres.passwordSecretName" . }}
{{- end }}

{{/*
Beads token secret name (daemon auth token)
*/}}
{{- define "gasboat.beads.tokenSecretName" -}}
{{- .Values.beads.tokenSecret }}
{{- end }}

{{/* ===== NATS component helpers ===== */}}

{{/*
NATS fully qualified name
*/}}
{{- define "gasboat.nats.fullname" -}}
{{- printf "%s-nats" (include "gasboat.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
NATS labels
*/}}
{{- define "gasboat.nats.labels" -}}
{{ include "gasboat.labels" . }}
app.kubernetes.io/component: nats
{{- end }}

{{/*
NATS selector labels
*/}}
{{- define "gasboat.nats.selectorLabels" -}}
{{ include "gasboat.selectorLabels" . }}
app.kubernetes.io/component: nats
{{- end }}

{{/*
NATS URL — returns nats://{svc}:4222 when nats.enabled, else empty string.
*/}}
{{- define "gasboat.natsURL" -}}
{{- if .Values.nats.enabled -}}
{{- printf "nats://%s:4222" (include "gasboat.nats.fullname" .) -}}
{{- end -}}
{{- end }}

{{/* ===== Landing Page component helpers ===== */}}

{{/*
Landing Page fully qualified name
*/}}
{{- define "gasboat.landing.fullname" -}}
{{- printf "%s-landing" (include "gasboat.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Landing Page labels
*/}}
{{- define "gasboat.landing.labels" -}}
{{ include "gasboat.labels" . }}
app.kubernetes.io/component: landing
{{- end }}

{{/*
Landing Page selector labels
*/}}
{{- define "gasboat.landing.selectorLabels" -}}
{{ include "gasboat.selectorLabels" . }}
app.kubernetes.io/component: landing
{{- end }}

{{/* ===== Pod scheduling helpers ===== */}}

{{/*
Merged nodeSelector — combines global.nodeSelector with per-component nodeSelector.
Per-component values take precedence over global values.
Usage: include "gasboat.nodeSelector" (dict "local" .Values.beads.nodeSelector "global" .Values.global.nodeSelector)
*/}}
{{- define "gasboat.nodeSelector" -}}
{{- $merged := merge (deepCopy (.local | default dict)) (.global | default dict) -}}
{{- if $merged }}
nodeSelector:
  {{- toYaml $merged | nindent 2 }}
{{- end }}
{{- end }}
