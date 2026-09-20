# Ternal HA + Rhiza Operator

## 개요

Ternal HA는 data.mode=ha로 3-voter StatefulSet + emptyDir(no-PVC) + 공유 오브젝트 저장소를 사용한다.
Operator는 opt-in이며 operator.enabled=true로 별도 Deployment로 실행한다. 현재 지원 플랫폼은 linux/amd64이다.

기존 standalone 배포는 HA로 전환할 수 없다. HA는 불변 identity를 사용하므로 새 클러스터를 처음부터 배포해야 한다.

## CRD 설치 (v0.15.0 고정)

Operator 사용 전 CRD를 먼저 설치한다:

    kubectl apply -f https://raw.githubusercontent.com/mrchypark/rhiza/v0.15.0/deploy/operator/crd.yaml
    kubectl apply -f https://raw.githubusercontent.com/mrchypark/rhiza/v0.15.0/deploy/operator/cluster-crd.yaml
    kubectl apply -f https://raw.githubusercontent.com/mrchypark/rhiza/v0.15.0/deploy/operator/fence-crd.yaml

## Operator 활성화

helm template 예시 (필수 필드 포함):

    helm template ternal deploy/helm/ternal \
      --namespace ternal \
      --set image.tag=YOUR_UPDATED_TERNAL_IMAGE_TAG \
      --set data.mode=ha \
      --set-string data.clusterID=ternal-ha \
      --set data.objectStore.provider=gcs \
      --set data.objectStore.bucket=your-bucket \
      --set-string data.trustAnchorConfigMap=ternal-trust \
      --set-string serviceAccountName=ternal-runtime \
      --set-string secrets.existingSecret=ternal-secrets \
      --set operator.enabled=true

YOUR_UPDATED_TERNAL_IMAGE_TAG는 이번 변경 사항을 포함해 빌드한 Ternal 이미지 태그여야 한다.

GCS Workload Identity 사용 시 operator.serviceAccountAnnotations에 GCP 서비스 계정을 바인딩한다:

    operator:
      enabled: true
      serviceAccountAnnotations:
        iam.gke.io/gcp-service-account: gcs-sa@proj.iam.gserviceaccount.com

## 관찰 모드

recoveryID가 비어 있으면 관찰 전용 요청이다. Operator는 읽기 전용이 아니다: 네임스페이스의 복구 CR을 계속 reconcile하며, 확인된 fence가 있는 CR은 대상 세대를 예약하고 소스 아카이브를 영구 seal한 뒤 워크로드를 변경할 수 있다. Ternal은 operator가 다시 쓰는 `RHIZA_*` 값을 읽지 않으므로 그 세대 전환을 완료할 수 없다. 세대 전환 경로가 자격 검증될 때까지 recoveryID를 비워 둔다.

    apiVersion: rhiza.mrchypark.dev/v1alpha1
    kind: RhizaRecovery
    metadata:
      name: ternal-api
      namespace: YOUR_NAMESPACE
    spec:
      statefulSet: ternal
      container: ternal-api
      sourceClusterID: YOUR_CLUSTER_ID
      durability: before-ack
      recoveryID: ""

kubectl get rhizarecoveries -n YOUR_NAMESPACE 으로 peer Ready/Quorum과 상태를 확인한다.

## 수동 복구 절차

자동 복구(automaticRecovery=true)는 아직 자격 검증되지 않았다. Helm 템플릿에서 자동으로 reject한다.

전체 세대 복구는 아직 검증되지 않았다. 대상 Ternal 시작 전 별도의 신뢰된 외부 fencing과 복구 작업에 맞춘 trust-anchor 전환이 모두 필요하다. 전환은 epoch, token, pending 증거와 단조 증명(monotonic proof)을 보존해야 하며, 조건을 충족하지 못하면 대상은 fail-closed된다. Ternal의 새 recovery-anchor 연동은 아직 구현되어 있지 않다. 이 설명은 소스 archive 확보 전에 trust anchor를 변경하라는 절차가 아니다.


## v0.15.0 복구 앵커 현황

Rhiza v0.15.0은 Coordinator의 Activate/Verify, CheckBinding(Ternal 시작 시 바인딩 검증), UpdateApplication(Ternal 쓰기 경로에서 애플리케이션 상태 갱신)을 제공한다. upstream 복구 앵커 문서는 docs/recovery-anchor.md (v0.15.0)를 참고한다: https://github.com/mrchypark/rhiza/blob/v0.15.0/docs/recovery-anchor.md

그러나 Ternal 사용에 필요한 아래 구성 요소는 아직 구현되지 않았다:

- Ternal 어댑터: recoveryanchor.Record의 EvidenceFormat/Evidence/Generation/Transition 구조를 사용하는 앵커 변환 계층. 기존 Ternal configmap의 flat Format/ClusterID/StorageID/Epoch/Token/Pending 필드를 증거 보존하면서 마이그레이션하는 레거시 호환 어댑터가 필요하다.
- 앱 전용 증거 검증기: Rhiza가 복원·검증한 아카이브에서 Ternal trust_state를 읽고, 외부 앵커의 epoch/token/pending 증거와 비교하는 검증기가 필요하다.
- HTTPS 앵커 서비스: TLS 인증 및 인증 토큰 기반 접근 제어가 포함된 앵커 서버. Operator 환경 변수 RHIZA_RECOVERY_ANCHOR_URL, RHIZA_RECOVERY_ANCHOR_TOKEN_FILE, RHIZA_RECOVERY_ANCHOR_CA_FILE의 설정 및 전달 경로.
- CR spec.anchorID: 복구 CR에서 앵커 서비스를 지정하는 필드 연결.
- 멤버 교체 설정: rhiza.Config의 EnableReconfiguration과 Learner 필드는 이미 존재하지만, Ternal의 rhizaConfigFromEnv가 TERNAL_DATA_ENABLE_RECONFIGURATION 및 TERNAL_DATA_LEARNER를 읽어 해당 필드에 연결하지 않는다. 환경 변수에서 Config로의 검증된 파싱과 learner 시작 경로가 필요하다.
- 펜싱 실행기: fencing 로직 실행 및 노드 격리 처리.
- 실제 E2E 검증: 복구 시나리오 전체를 통과하는 종단간 테스트.

자동 복구(automaticRecovery) 가드는 그대로 유지된다.
## 자격 증명

Operator Deployment는 기존 Ternal 시크릿의 TERNAL_OBJECT_STORE_ACCESS_KEY, TERNAL_OBJECT_STORE_SECRET_KEY, TERNAL_OBJECT_STORE_SESSION_TOKEN을 각각 RHIZA_OBJSTORE_ACCESS_KEY, RHIZA_OBJSTORE_SECRET_KEY, RHIZA_OBJSTORE_SESSION_TOKEN 환경 변수로 매핑한다. 이 자격 증명 Secret 키 참조만 optional이다.
GCS Workload Identity를 사용하면 시크릿 키를 생략할 수 있다.

## 검증

`deploy/vind/operator-recovery-e2e.sh`가 실제 vCluster에서 3-voter HA, operator 관찰, 수동 세대 복구를 순서대로 확인한다. 로컬 이미지와 pinned 업스트림 소스로 이미지를 만들어 클러스터에 넣고, MinIO를 공유 오브젝트 저장소로 사용한다.

현재 이 스크립트는 1단계에서 멈춘다. 문서화된 방식으로 앵커를 준비한 greenfield HA 릴리스가 trust-anchor migration fence에서 교착된다(#102). 관찰·복구 단계는 그 문제가 해결된 뒤에 자격 검증된다.

## 참고

- Embedded Operator Guide: https://github.com/mrchypark/rhiza/blob/v0.15.0/docs/embedded-operator.md
- Operator README: https://github.com/mrchypark/rhiza/blob/v0.15.0/deploy/operator/README.md
- 복구 후 helm upgrade 하면 복구된 세대가 손상될 수 있다. CR과 상태를 먼저 확인한다.

## Ternal 설정과의 경계

Ternal 자체의 공개 설정은 `TERNAL_*` 역할 이름만 사용한다. 위 `RHIZA_*` 변수는 operator 컨테이너(업스트림 바이너리)의 자체 인터페이스이며, operator 컨트롤러는 대상 파드의 환경에서 cluster ID, membership, admin token, 데이터 디렉터리, object store 위치를 읽는다. 차트는 `data.clusterID`, `data.objectStore`, `secrets.existingSecret` 값으로 그 이름들을 만들어 operator Deployment와 HA 파드에 넣는다. 값을 정하는 곳은 여전히 Ternal 설정이고, `RHIZA_*` 이름은 operator 소유 차트 파일(`deploy/helm/ternal/templates/operator-*`), operator 식별자 bridge(`internal/operatoridentity`), 이 문서에만 나타난다. Ternal API 파드는 operator와 `TERNAL_OPERATOR_BIND` 엔드포인트로 통신한다.

operator가 복구 세대를 시작할 때 다시 쓰는 값은 `internal/operatoridentity`가 프로세스 시작 시 Ternal 설정 이름으로 옮긴다. 그 파일 하나만 업스트림 이름을 알고, 사용자가 설정하는 값은 계속 `TERNAL_*`이다. 이 bridge는 operator가 파드 환경을 다시 쓰지 않는 한 아무것도 바꾸지 않는다.

`TERNAL_OPERATOR_BIND`는 엔드포인트마다 인증이 다르다. 관리 엔드포인트는 admin token을 요구하지만 `/recovery/status`는 토큰 없이 응답한다. 그래서 차트의 NetworkPolicy가 9091 포트를 operator 파드로만 제한하며, 이 리스너를 클러스터나 서비스로 넓게 열지 않는다.

HA 파드에 실리는 값은 operator의 검증 입력이며 Ternal 엔진 설정이 아니다. Ternal은 `TERNAL_*` 값만 읽으므로, operator가 세대 사이에 갱신하는 `RHIZA_*` 값만으로는 파드의 엔진 설정이 바뀌지 않는다. 그래서 세대 전환(수동 복구 포함)은 아직 자격 검증되지 않았고, 관찰 모드만 확인된 범위다.
