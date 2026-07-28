{{- define "hal-k8s-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "hal-k8s-operator.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "hal-k8s-operator.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
app.kubernetes.io/name: {{ include "hal-k8s-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}

{{- define "hal-k8s-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "hal-k8s-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: controller-manager
{{- end }}

{{- define "hal-k8s-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "hal-k8s-operator.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "hal-k8s-operator.imageTag" -}}
{{- default .Chart.AppVersion .Values.image.tag }}
{{- end }}

{{- define "hal-k8s-operator.image" -}}
{{- printf "%s:%s" .Values.image.repository (include "hal-k8s-operator.imageTag" .) }}
{{- end }}

{{- define "hal-k8s-operator.triageImage" -}}
{{- if .Values.triage.image }}
{{- .Values.triage.image }}
{{- else }}
{{- include "hal-k8s-operator.image" . }}
{{- end }}
{{- end }}

{{- define "hal-k8s-operator.fixImage" -}}
{{- if .Values.fix.image }}
{{- .Values.fix.image }}
{{- else }}
{{- printf "%s-fix:%s" .Values.image.repository (include "hal-k8s-operator.imageTag" .) }}
{{- end }}
{{- end }}
