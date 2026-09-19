# IED 실제 검증 결과 — v0.3.15-rc.8

> 2026-09-09: 동일한 소스를 정식 [v0.3.15](https://github.com/mrchypark/ternal/releases/tag/v0.3.15)로 발행했습니다. 네이티브 번들 8개와 trustguard는 rc8과 바이트 단위로 동일합니다. 정식 이미지의 체크섬·provenance·서명 검증 결과는 [정식 릴리스 기록](../output/releases/v0.3.15/verification.json)에 있습니다. 아래 실환경 결과는 rc8에서 수행한 기록입니다.

공개 `mrchypark/ternal` 릴리스로 아래 기능·보안 시나리오를 검증했습니다. `v0.2.7`은 실패 기준선으로 보존하고 수정된 공개 후보를 사용했습니다. Rauthy 실제 로그인으로 검증했으며 dev-header나 로컬 재빌드 애플리케이션을 성공 판정에 사용하지 않았습니다.

- 릴리스: https://github.com/mrchypark/ternal/releases/tag/v0.3.15-rc.8
- 소스: `299147e9827de718d8fd1191937e8162978faa2e`
- 공개 workflow: `34263785910` — 성공
- 서명·태그·실행 이미지: `ghcr.io/mrchypark/ternal@sha256:437df0868a6ab56665a7564e6b38c923dfe8e2d77fae1aea87f86fd9df638b1e`
- 마지막 Helm revision: `8`. Ternal 데이터는 PVC 없이 GCS와 emptyDir를 사용했습니다.
- 증거: [검증 파일](../output/ied-qualification-20260909/). rc8 결과와 이전 후보에서 확인한 변경 없는 항목을 구분했습니다. 표의 **이전 검증**은 [누적 실측 기록](ied-v0.3.15-rc.1-qualification-2026-09-08.md)을 근거로 하며, 보존한 JSON 묶음만으로 그 모든 세부 항목을 독립 검증할 수 있다는 뜻은 아닙니다.

| 시나리오 | 확인 결과 | 검증 범위 |
|---|---|---|
| 1. 릴리스 무결성 | 체크섬 16개, 네이티브 번들 8개, provenance, cosign, 태그와 실행 digest 일치 | rc8 |
| 2. 배포 | 최초 설치의 health/TLS/Gateway·보안 컨텍스트 확인; rc8 API Ready·relay SSH·no-PVC·이전 이미지 admission 거부 | 최초 설치·보안 컨텍스트는 이전 누적 기록; rc8은 업그레이드·연결 재확인 |
| 3. 관리자 인증 | 실제 Rauthy authorization-code, 관리자 그룹 연결, 로그아웃·cookie 재사용 거부 | OIDC 상세 경계는 이전 공개 후보; 로그인·로그아웃은 rc8 재확인 |
| 4. 제조 | 배치 max2에 연결된 일회성 토큰, 순차 serial, 정확한 장치 identity 결합, 재사용·초과·만료 거부 | rc6 상세 검증 + rc8 새 장치 등록 |
| 5. 에이전트 | 공개 번들 등록·재기동·identity 유지, 서명 heartbeat, 명시적 경로 게시, 잘못된 서명·identity·timestamp 거부 | 이전 실패 시나리오 + rc8 수명주기 |
| 6. 일반 사용자·키 | 실제 browser/device flow, 로컬 session 세 필드와 mode600, provider token 없음, 키 정규화·소유권 격리 | 이전 키 경계 + rc8 device flow·CLI logout |
| 7. 정책·가시성 | 그룹·태그·SSH 사용자 정책, 미허용·만료·잘못된 SSH 사용자 거부, 일반 목록 endpoint 비공개 | 이전 상세 검증 + rc8 정책 연결·거부 |
| 8. relay SSH | 실제 banner, stdin/stdout/stderr, exit23, 256KiB EOF/drain·정확한 262160바이트·exit29 | rc8, UDP 차단으로 relay 강제 |
| 9. direct SSH | 실제 direct 왕복 및 큰 입력 drain, 잘못된 주소 실패, endpoint-only 거부 | rc8, relay TCP 차단으로 direct 강제 |
| 10. 보안 실패 | grant/bearer/endpoint 경계, 실제 300초 만료, 잘못된 pin 및 실제 서버 host key 교체 즉시 거부 | rc8 + 이전 OIDC·무grant 실제 연결 실패 검증 |
| 11. authorized_keys | 승인 키만 동기화, 원자 교체·generation rollback/equivocation 거부, 실제 grant 만료 후 키 제거·세대 증가 | 이전 파일 경계 + rc8 동기화·만료 |
| 12. 폐기 | 실행 표식 이후 DELETE200, 기존 SSH 종료, agent/transport 종료, heartbeat·discovery·새 grant·callback 거부 | rc8, API 및 폐기된 agent 재시작 후에도 유지 |
| 13. 포털·감사 | SSR/htmx, 권한별 메뉴와 403, focus, 모바일390px, escaping, 허용·거부·제조·정책·폐기 감사 | 이전 상세 UX + rc8 smoke·감사 |
| 14. 복구·정리 | Pod 재시작 후 폐기·로그아웃 유지, 과거 전체 object prefix 복원 시 trust mismatch/never Ready, 외부 anchor 불변 | rc8 rollback 및 재시작; 정리 결과는 아래 참조 |

## 원래 로고와 파비콘

기존 `frontend/src/assets/brand` PNG를 SSR의 `/assets/ternal-logo-640.png`, `/assets/ternal-icon-256.png`에 적용했습니다. rc8 서버가 제공한 두 파일이 원본과 바이트 단위로 일치합니다. [실제 모바일 화면](../output/ied-qualification-20260909/portal-mobile.png).

## 한계와 사고 기록

- 모든 상세 테스트를 rc8에서 처음부터 반복한 것은 아닙니다. 변경되지 않은 OIDC 실패 경계, 키 정규화·소유권, 파일 원자 교체 등의 기존 공개 후보 증거와 rc8 영향 범위 재검증을 함께 사용했습니다.
- 추가적인 **HTTP 취소의 정확한 pending-CAS 경계 포착은 실제 환경에서 판정 불가**였습니다. 작업이 kill 신호보다 먼저 완료됐습니다. 두 시도 이후 API Ready200, pending 없음, agent 정상 상태는 확인했지만 이를 취소 경계 실증 통과라고 부르지 않습니다. 해당 경계를 강제로 만드는 결정적 회귀 테스트와 전체 race 테스트는 통과했습니다.
- 이전 후보 검증 중 테스트 비밀번호가 Playwright 오류 출력에 포함된 사고가 있었습니다. 비밀번호를 교체하고 실제 재로그인을 확인했습니다. 이전 CSRF/bootstrap 노출도 폐기·교체했습니다. 과거 도구 기록 삭제나 전체 수행 중 비밀값 노출이 전혀 없었다고 주장하지 않습니다. 이후 오류 출력은 예외 종류만 남기도록 바꿨고, 내보낸 증거에서 알려진 자격증명 값 25개가 없는 것을 확인했습니다.
- rc2 전체 과거 데이터 복원, rc4 기존 SSH 폐기, rc7 요청 취소/인증 실패 분류 오류는 실패 기록입니다. rc8의 성공 결과로 과거 실패를 덮어쓰지 않았습니다.

## 정리

외부 정리 완료: 테스트 장치 8개를 모두 폐기한 뒤 Helm 릴리스 2개, `ternal-v027-qual` namespace, admission 리소스 6개, 전용 Gateway/TLS 리소스 5개와 **`gs://ternal-qual-20260908-602454948273` 버킷 하나**를 제거했습니다. 초기 기준선·Rauthy용 PVC 2개의 PV와 GCE 디스크 자동 삭제도 [별도 조회로 확인](../output/ied-qualification-20260909/legacy-volume-cleanup-verification.json)했습니다.

공유 `haproxy-gateway` 서비스, 기존 `ternal` namespace와 `gs://ternal-ied-602454948273` 버킷은 보존했습니다. 테스트 브라우저 context 29개를 닫고 자격증명·개인키·원시 진단 자료가 있던 임시 디렉터리 15개를 제거했습니다. [삭제·보존 확인](../output/ied-qualification-20260909/cleanup-result.json), [정확한 정리 목록](../output/ied-qualification-20260909/cleanup-scope.json).

상세 이력: [누적 검증 기록](ied-v0.3.15-rc.1-qualification-2026-09-08.md), [v0.2.7 기준선](ied-v0.2.7-qualification-2026-09-08.md).
