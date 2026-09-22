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

recoveryID가 비어 있으면 관찰 전용 요청이다. Operator는 읽기 전용이 아니다: 네임스페이스의 복구 CR을 계속 reconcile하며, 확인된 fence가 있는 CR은 대상 세대를 예약하고 소스 아카이브를 영구 seal한 뒤 워크로드를 변경할 수 있다. Ternal은 시작 시 operator가 갱신한 세대 ID와 멤버 설정을 `TERNAL_*` 설정에 반영한다. 수동 세대 복구는 아래 앵커 서비스 연결과 신뢰된 외부 fencing을 요구한다.

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

전체 세대 복구는 대상 Ternal 시작 전 별도의 신뢰된 외부 fencing과 복구 작업에 맞춘 trust-anchor 전환이 모두 필요하다. 전환은 epoch, token, pending 증거와 단조 증명(monotonic proof)을 보존해야 하며, 조건을 충족하지 못하면 대상은 fail-closed된다. 이 설명은 소스 archive 확보 전에 trust anchor를 변경하라는 절차가 아니다.


## v0.15.0 복구 앵커 현황

Rhiza v0.15.0은 Coordinator의 Activate/Verify, CheckBinding(Ternal 시작 시 바인딩 검증), UpdateApplication(Ternal 쓰기 경로에서 애플리케이션 상태 갱신)을 제공한다. upstream 복구 앵커 문서는 docs/recovery-anchor.md (v0.15.0)를 참고한다: https://github.com/mrchypark/rhiza/blob/v0.15.0/docs/recovery-anchor.md

Ternal은 위 구성 요소를 갖췄다. 어댑터는 기존 trust-anchor ConfigMap을 그대로 두고 flat 필드와 공통 Record를 한 번의 compare-and-swap으로 함께 쓴다(internal/store/trust_anchor_composite.go). 검증기는 복원된 아카이브의 trust_state에서 애플리케이션 쌍을 다시 읽어 동결된 증거와 비교하므로, 요청을 되풀이하는 대상은 커밋되지 않는다(internal/store/recovery_verifier.go). 앵커 서비스는 같은 이미지의 별도 실행 파일(ternal-anchor)과 별도 Deployment로 뜬다(internal/store/anchor_service.go, cmd/ternal-anchor). admission 정책은 전환을 앵커 서비스 계정으로만 허용한다.

아직 구현되지 않은 것은 멤버 교체(Ternal의 rhizaConfigFromEnv가 TERNAL_DATA_ENABLE_RECONFIGURATION과 TERNAL_DATA_LEARNER를 rhiza.Config로 옮기지 않는다)와 펜싱 실행기다. 대상의 voter endpoint가 소스와 다르면 operator가 거부하므로, 재구성 opt-in은 voter 교체 권한이 아니다.

자동 복구(automaticRecovery) 가드는 그대로 유지된다.

## 복구 앵커 활성화

세대 복구에는 앵커 서비스가 필요하다. 별도 Deployment로 떠야 복구된 StatefulSet이 unready이거나 0으로 줄어든 동안에도 살아 있다. 켜려면 HA 값에 anchor 블록을 더한다:

    anchor:
      enabled: true
      tlsSecretName: ternal-anchor-tls     # tls.crt, tls.key
      tokenSecretName: ternal-anchor-token # operator가 제시하는 bearer token
      tokenSecretKey: token
      serviceAccountName: ternal-anchor    # trustguard 렌더러에 준 값과 같아야 한다

tlsSecretName과 tokenSecretName은 미리 만들어 둔 Secret이다. 앵커 ID는 data.trustAnchorConfigMap과 같은 값이어야 하므로 차트가 그 값에서 유도한다: admission 정책이 앵커 컨테이너를 그 ConfigMap에 고정하기 때문이다. 앵커 파드는 /data를 마운트하지 않고 replicated store를 열지 않으며, operator 파드만 9191 포트로 접근할 수 있다.

operator 쪽은 RHIZA_RECOVERY_ANCHOR_URL(https만), RHIZA_RECOVERY_ANCHOR_TOKEN_FILE, RHIZA_RECOVERY_ANCHOR_CA_FILE로 그 서비스를 가리킨다. 복구 CR의 spec.anchorID는 그 앵커 ID와 같아야 하고, 대상 파드에는 같은 값과 RHIZA_ENABLE_RECONFIGURATION=true가 함께 실려야 한다. 관찰 전용 CR도 같은 spec.anchorID를 요구한다.
## 자격 증명

Operator Deployment는 기존 Ternal 시크릿의 TERNAL_OBJECT_STORE_ACCESS_KEY, TERNAL_OBJECT_STORE_SECRET_KEY, TERNAL_OBJECT_STORE_SESSION_TOKEN을 각각 RHIZA_OBJSTORE_ACCESS_KEY, RHIZA_OBJSTORE_SECRET_KEY, RHIZA_OBJSTORE_SESSION_TOKEN 환경 변수로 매핑한다. 이 자격 증명 Secret 키 참조만 optional이다.
GCS Workload Identity를 사용하면 시크릿 키를 생략할 수 있다.

## 검증

`deploy/vind/operator-recovery-e2e.sh`가 실제 vCluster에서 3-voter HA, operator 관찰, 수동 세대 복구를 순서대로 확인한다. 로컬 이미지와 pinned 업스트림 소스로 이미지를 만들어 클러스터에 넣고, MinIO를 공유 오브젝트 저장소로 사용한다.

검증 기준은 greenfield HA quorum(#102), 준비된 3 peer의 Observed 상태, 수동 세대 복구의 Complete 및 실제 3 voter Ready다. admission 정책을 켠 상태에서 기존 ConfigMap UID와 애플리케이션 증거를 보존하는지 확인하고, 복구 후 정상 쓰기가 epoch를 한 단계 올리는지 검사한다. 스크립트의 성공 종료를 확인하기 전에는 배포 환경의 복구 검증 완료로 간주하지 않는다.

## 참고

- Embedded Operator Guide: https://github.com/mrchypark/rhiza/blob/v0.15.0/docs/embedded-operator.md
- Operator README: https://github.com/mrchypark/rhiza/blob/v0.15.0/deploy/operator/README.md
- 복구 후 helm upgrade 하면 복구된 세대가 손상될 수 있다. CR과 상태를 먼저 확인한다.

## Ternal 설정과의 경계

Ternal 자체의 공개 설정은 `TERNAL_*` 역할 이름만 사용한다. 위 `RHIZA_*` 변수는 operator 컨테이너(업스트림 바이너리)의 자체 인터페이스이며, operator 컨트롤러는 대상 파드의 환경에서 cluster ID, membership, admin token, 데이터 디렉터리, object store 위치를 읽는다. 차트는 `data.clusterID`, `data.objectStore`, `secrets.existingSecret` 값으로 그 이름들을 만들어 operator Deployment와 HA 파드에 넣는다. 값을 정하는 곳은 여전히 Ternal 설정이고, `RHIZA_*` 이름은 operator 소유 차트 파일(`deploy/helm/ternal/templates/operator-*`), operator 식별자 bridge(`internal/operatoridentity`), 이 문서에만 나타난다. Ternal API 파드는 operator와 `TERNAL_OPERATOR_BIND` 엔드포인트로 통신한다.

operator가 복구 세대를 시작할 때 다시 쓰는 값은 `internal/operatoridentity`가 프로세스 시작 시 Ternal 설정 이름으로 옮긴다. 그 파일 하나만 업스트림 이름을 알고, 사용자가 설정하는 값은 계속 `TERNAL_*`이다. 이 bridge는 operator가 파드 환경을 다시 쓰지 않는 한 아무것도 바꾸지 않는다.

`TERNAL_OPERATOR_BIND`는 엔드포인트마다 인증이 다르다. 관리 엔드포인트는 admin token을 요구하지만 `/recovery/status`는 토큰 없이 응답한다. 그래서 차트의 NetworkPolicy가 9091 포트를 operator 파드로만 제한하며, 이 리스너를 클러스터나 서비스로 넓게 열지 않는다.

HA 파드의 `RHIZA_*` 값은 operator의 검증 입력이다. 세대 전환 시 Ternal의 시작 경로가 operator가 갱신한 ID·멤버·관리 토큰을 `TERNAL_*` 값으로 옮긴다. 복구 앵커가 대상 바인딩을 커밋하고 복원 데이터의 trust pair가 확인돼야 준비 상태가 된다. 자동 복구와 voter 교체는 이 수동 세대 복구 경로의 검증 범위에 포함되지 않는다.
