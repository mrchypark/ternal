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
{{- dict
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
  "secretName" (default .Release.Name .Values.secrets.existingSecret)
  | toJson | sha256sum | trunc 32 -}}
{{- end -}}

{{- define "ternal.dataConfigMapName" -}}
{{- printf "%s-data-%s" (trunc 25 .Release.Name | trimSuffix "-") (include "ternal.dataIdentity" .) -}}
{{- end -}}
