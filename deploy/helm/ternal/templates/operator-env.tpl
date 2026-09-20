{{/*
The upstream rhiza controller reads the target pod's environment: identity,
membership, admin token, data directory, and the object-store location it
compares against its own. A Ternal StatefulSet has to carry those upstream
names for the operator to observe it.

This file is the only place the chart renders upstream variable names into
Ternal's pods, and every value is derived from Ternal's own configuration
(data.clusterID, data.objectStore, secrets.existingSecret). Ternal's own
configuration surface stays TERNAL_*, and scripts/check-public-config.sh
exempts only operator-* chart files and the operator guide.
*/}}
{{- define "ternal.operatorEnv" -}}
{{- $data := default dict .Values.data -}}
{{- $objectStore := default dict $data.objectStore -}}
{{- $clusterID := required "data.clusterID is required when the operator is enabled" $data.clusterID -}}
{{- $secretName := .Values.secrets.existingSecret | default .Release.Name -}}
- name: RHIZA_NODE_ID
  valueFrom:
    fieldRef:
      fieldPath: metadata.name
- name: RHIZA_DATA_DIR
  value: /data/ternal
- name: RHIZA_CLUSTER_ID
  value: {{ $clusterID | quote }}
- name: RHIZA_CLUSTER_MEMBERS
  valueFrom:
    secretKeyRef:
      name: {{ $secretName }}
      key: TERNAL_DATA_CLUSTER_MEMBERS
- name: RHIZA_ADMIN_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ $secretName }}
      key: TERNAL_DATA_ADMIN_TOKEN
- name: RHIZA_OBJSTORE_PROVIDER
  value: {{ required "data.objectStore.provider is required when the operator is enabled" $objectStore.provider | quote }}
- name: RHIZA_OBJSTORE_ENDPOINT
  value: {{ default "" $objectStore.endpoint | quote }}
- name: RHIZA_OBJSTORE_BUCKET
  value: {{ required "data.objectStore.bucket is required when the operator is enabled" $objectStore.bucket | quote }}
- name: RHIZA_OBJSTORE_PREFIX
  value: {{ default (printf "clusters/%s" $clusterID) $objectStore.prefix | quote }}
- name: RHIZA_OBJSTORE_DURABILITY
  value: {{ default "before-ack" $objectStore.durability | quote }}
{{- end -}}
