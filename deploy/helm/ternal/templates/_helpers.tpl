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
{{- if eq (default "" $objectStore.provider) "azure" -}}
{{- $_ := set $identity "azureStorageAccount" (default "" (default dict $objectStore.azure).storageAccount) -}}
{{- $_ := set $identity "azureBlobEndpoint" (include "ternal.azureBlobEndpoint" .) -}}
{{- end -}}
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
{{ include "ternal.azurePublicEnv" . }}
{{- end -}}

{{/* Public configuration is direct env: an old runtime Secret cannot redirect it. */}}
{{- define "ternal.singleNodeEnv" -}}
{{- $config := include (print .Template.BasePath "/configmap.yaml") . | fromYaml -}}
{{- range $name, $value := $config.data }}
- name: {{ $name }}
  value: {{ $value | toString | quote }}
{{- end }}
- name: TERNAL_DATA_LOCAL
  value: "0"
- name: TERNAL_DATA_MULTI_NODE
  value: "0"
- name: TERNAL_DATA_NODE_ID
  value: node-1
- name: TERNAL_DATA_CLUSTER_MEMBERS
  value: ""
- name: TERNAL_DATA_EXPECTED_MEMBER_IDS
  value: ""
- name: TERNAL_DATA_BIND_ADDR
  value: "127.0.0.1:0"
- name: TERNAL_DATA_PEER_ADDR
  value: "127.0.0.1:0"
- name: TERNAL_OBJECT_STORE_DIR
  value: ""
- name: TERNAL_OPERATOR_BIND
  value: ""
- name: TERNAL_TRUST_ANCHOR_CONFIGMAP
  value: {{ .Values.data.trustAnchorConfigMap | quote }}
- name: TERNAL_TRUST_ANCHOR_NAMESPACE
  value: {{ .Release.Namespace | quote }}
{{- end -}}

{{- define "ternal.azureBlobEndpoint" -}}
{{- $azure := default dict .Values.data.objectStore.azure -}}
{{- default (printf "https://%s.blob.core.windows.net" (default "" $azure.storageAccount)) $azure.blobEndpoint -}}
{{- end -}}

{{- define "ternal.objectStoreCredentialKeys" -}}
{{- if eq .Values.data.objectStore.provider "azure" -}}
{{- list "TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT_KEY" "TERNAL_OBJECT_STORE_AZURE_CONNECTION_STRING" "TERNAL_OBJECT_STORE_AZURE_CLIENT_SECRET" | toJson -}}
{{- else -}}
{{- list "TERNAL_OBJECT_STORE_ACCESS_KEY" "TERNAL_OBJECT_STORE_SECRET_KEY" "TERNAL_OBJECT_STORE_SESSION_TOKEN" | toJson -}}
{{- end -}}
{{- end -}}

{{- define "ternal.azurePublicEnv" -}}
{{- if eq .Values.data.objectStore.provider "azure" -}}
{{- $azure := default dict .Values.data.objectStore.azure -}}
- name: TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT
  value: {{ $azure.storageAccount | quote }}
- name: TERNAL_OBJECT_STORE_AZURE_BLOB_ENDPOINT
  value: {{ include "ternal.azureBlobEndpoint" . | quote }}
- name: TERNAL_OBJECT_STORE_AZURE_TENANT_ID
  value: {{ default "" $azure.tenantID | quote }}
- name: TERNAL_OBJECT_STORE_AZURE_CLIENT_ID
  value: {{ default "" $azure.clientID | quote }}
- name: TERNAL_OBJECT_STORE_AZURE_USER_ASSIGNED_ID
  value: {{ default "" $azure.userAssignedID | quote }}
- name: TERNAL_OBJECT_STORE_AZURE_EMULATOR
  value: "0"
{{- end -}}
{{- end -}}
