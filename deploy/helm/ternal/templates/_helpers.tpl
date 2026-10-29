{{- define "ternal.dataIdentity" -}}
{{- $data := default dict .Values.data -}}
{{- $mode := default "standalone" $data.mode -}}
{{- $clusterID := default "ternal" $data.clusterID -}}
{{- $schemaVersion := int $data.schemaVersion -}}
{{- $objectStore := default dict $data.objectStore -}}
{{- $prefix := default (printf "clusters/%s" $clusterID) $objectStore.prefix -}}
{{- $memberIDs := "" -}}
{{- $peerPort := 0 -}}
{{- if eq $mode "ha" -}}
{{- $clusterID = required "data.clusterID is required for data.mode=ha" $data.clusterID -}}
{{- $ha := default dict $data.ha -}}
{{- $memberIDs = printf "%s-0,%s-1,%s-2" .Release.Name .Release.Name .Release.Name -}}
{{- $peerPort = int (default 9090 $ha.peerPort) -}}
{{- end -}}

{{- $identity := dict
  "mode" $mode
  "clusterID" $clusterID
  "schemaVersion" $schemaVersion
  "memberIDs" $memberIDs
  "peerPort" $peerPort
  "objectStoreProvider" (default "" $objectStore.provider)
  "objectStoreEndpoint" (default "" $objectStore.endpoint)
  "objectStoreBucket" (default "" $objectStore.bucket)
  "objectStorePrefix" $prefix
  "objectStoreRegion" (default "" $objectStore.region)
  "objectStoreInsecure" ($objectStore.insecure | default false)
  "objectStoreDurability" (default "before-ack" $objectStore.durability)
  "secretName" (default .Release.Name .Values.secrets.existingSecret) -}}
{{- if eq $mode "local" -}}
{{- $_ := set $identity "localArchiveNamespace" (clean (printf "%s/%s/local/v1" $prefix $clusterID)) -}}
{{- $_ := set $identity "localStorageDomain" "ternal-local-v1" -}}
{{- end -}}
{{- $identity | toJson | sha256sum | trunc 32 -}}
{{- end -}}

{{- define "ternal.dataConfigMapName" -}}
{{- printf "%s-data-%s" (trunc 25 .Release.Name | trimSuffix "-") (include "ternal.dataIdentity" .) -}}
{{- end -}}

{{/* Data-only, direct values shared by Local recovery init and API. */}}
{{- define "ternal.localDataEnv" -}}
{{- $data := .Values.data -}}
{{- $objectStore := $data.objectStore -}}
- name: TERNAL_DATA_LOCAL
  value: "1"
- name: TERNAL_DATA_MULTI_NODE
  value: "0"
- name: TERNAL_DATA_DIR
  value: /data/ternal
- name: TERNAL_DATA_CLUSTER_ID
  value: {{ $data.clusterID | quote }}
- name: TERNAL_DATA_NODE_ID
  value: node-1
- name: TERNAL_LOCAL_PREFLIGHT_STAGING_DIR
  value: /staging
- name: TERNAL_LOCAL_PREFLIGHT_RECEIPT_FILE
  value: /staging/preflight.json
- name: TERNAL_LOCAL_PREFLIGHT_SCOPE
  valueFrom:
    fieldRef:
      apiVersion: v1
      fieldPath: metadata.uid
- name: TERNAL_LOCAL_WRITER_GC_QUIESCENCE_ACK
  value: "1"
- name: TERNAL_TRUST_ANCHOR_CONFIGMAP
  value: {{ $data.trustAnchorConfigMap | quote }}
- name: TERNAL_TRUST_ANCHOR_NAMESPACE
  value: {{ .Release.Namespace | quote }}
- name: TERNAL_OBJECT_STORE_PROVIDER
  value: {{ $objectStore.provider | quote }}
- name: TERNAL_OBJECT_STORE_ENDPOINT
  value: {{ default "" $objectStore.endpoint | quote }}
- name: TERNAL_OBJECT_STORE_BUCKET
  value: {{ $objectStore.bucket | quote }}
- name: TERNAL_OBJECT_STORE_PREFIX
  value: {{ default (printf "clusters/%s" $data.clusterID) $objectStore.prefix | quote }}
- name: TERNAL_OBJECT_STORE_REGION
  value: {{ default "" $objectStore.region | quote }}
- name: TERNAL_OBJECT_STORE_INSECURE
  value: {{ ternary "true" "false" ($objectStore.insecure | default false) | quote }}
- name: TERNAL_OBJECT_STORE_DURABILITY
  value: before-ack
{{- end -}}
