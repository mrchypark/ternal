# Security Review: Ternal @ b266a74

**Review commit:** `b266a747806a0c5f31fa9f09dc2b46e0d58533fc`
**Verification base:** `52df505` (one transport commit ahead; all findings confirmed)
**Date:** 2026-09-19

## Verification Summary

All 28 findings (TERN-B266-001 through -028), 5 design boundaries (D01-D05), and 3 performance hypotheses (H01-H03) were verified against the source code. **Every code-confirmed finding in the review is accurate** — the described causal paths exist in the pinned source.

---

## P1 — Release Blockers

### TERN-B266-001: ProxyCommand shell injection via IPv6 zone identifiers

**Status:** VERIFIED
**Location:** `internal/core/core.go:375-381` (`validDirectAddress`), `core.go:302` (`appendRouteArgs`)

`validDirectAddress` uses `net.ResolveTCPAddr("tcp", value)` which accepts IPv6 zone identifiers (e.g., `fe80::1%eth0`). The accepted address is concatenated into a shell command at `core.go:302` (`command += " --direct-address " + addr`) without shell escaping. This command is placed into `ProxyCommand=` at `core.go:190` and `core.go:217`. A device controlling its signed discovery can inject shell metacharacters through the zone identifier.

### TERN-B266-002: SSH config injection via host names

**Status:** VERIFIED
**Location:** `internal/api/api.go:700` (`handleSSHConfig`), `internal/store/store.go:1427` (`EnrollDevice`)

The SSH config formatter at `api.go:700` places `h.Name` directly into a `Host` directive. During enrollment (`store.go:1427`), the serial number becomes the host name. No control-character or newline validation exists on either path.

### TERN-B266-003: SSH keys outlive database grants

**Status:** VERIFIED
**Location:** `internal/store/store.go:808-842` (`authorizedKeysSnapshotForHost`), `cmd/ternal-agent/main.go:245-305` (`syncAuthorizedKeys`)

The server filters expired grants (`store.go:817`: `g.expires_at > ?`), but the agent installs ordinary authorized-key lines with no locally enforced expiry (`main.go:290`: `atomicWrite(path, body, 0600)`). Agent death or connectivity failure preserves the old file indefinitely.

---

## P2 — Snapshot Correctness and Resource Boundaries

### TERN-B266-004: Older key snapshot can receive newer generation

**Status:** VERIFIED
**Location:** `internal/api/api.go:1091-1128` (`handleAgentAuthorizedKeys`)

Reading the key/grant set (`store.AuthorizedKeysSnapshotForHost` at line 1107) and publishing its generation (`store.AuthorizedKeysGeneration` at line 1118) are separate operations. A concurrent request can interleave between these calls.

### TERN-B266-005: Aggregate key sets can exceed agent snapshot limit

**Status:** VERIFIED
**Location:** `cmd/ternal-agent/main.go:266` (`io.LimitReader(response.Body, 1<<20)`)

The agent reads only the first 1 MiB of the response. No admission limit exists on key/grant registration that would keep snapshots within this bound.

### TERN-B266-006: Snapshot SQL produces keys-by-grants cross product

**Status:** VERIFIED
**Location:** `internal/store/store.go:809-842` (`authorizedKeysSnapshotForHost`)

The SQL query selects `DISTINCT k.public_key, g.id` pairs, then deduplicates each dimension in Go. For K keys and G grants, this yields K×G rows before deduplication.

---

## P2 — Agent Recovery and Local State

### TERN-B266-007: Crash leaves synchronization lock permanently held

**Status:** VERIFIED
**Location:** `cmd/ternal-agent/main.go:307-319` (`acquireSyncLock`)

The lock is a directory (`os.Mkdir`) removed only by a deferred cleanup function. SIGKILL or power loss leaves it behind, blocking all future synchronizations.

### TERN-B266-008: Root-run synchronization makes key files unreadable

**Status:** VERIFIED
**Location:** `cmd/ternal-agent/main.go:545-571` (`atomicWrite`)

Files are created with mode `0600` and no ownership assignment. A root-run agent managing a non-root account's authorized-key file changes it to root-owned `0600`.

### TERN-B266-009: Successful acknowledgements don't establish rename durability

**Status:** VERIFIED
**Location:** `cmd/ternal-agent/main.go:545-571` (`atomicWrite`), `internal/deviceauth/deviceauth.go:127-153` (`atomicWrite`)

Both `atomicWrite` implementations sync the file but do not sync the containing directory. Directory entry durability is not guaranteed before acknowledgement.

### TERN-B266-010: Enrollment cannot recover idempotently after committed response loss

**Status:** VERIFIED
**Location:** `cmd/ternal-agent/main.go:199-228` (`enroll`), `internal/store/store.go:1352-1441` (`EnrollDevice`)

The token is consumed atomically with host/device creation, but the identity is persisted only after receiving the response (`main.go:223`). A lost response leaves a committed enrollment that cannot be retried with the same token.

### TERN-B266-011: Deleting a host leaves orphaned device identities

**Status:** VERIFIED
**Location:** `internal/store/store.go:679-689` (`DeleteHost`), `store.go:1527-1541` (`GetDeviceBySerial`)

`DeleteHost` deletes only the host row. No cascading delete exists for devices. `GetDeviceBySerial` performs an unordered lookup, creating ambiguity when a serial is reused.

---

## P2 — End-to-End SSH Flow

### TERN-B266-012: Exported SSH configuration never creates SSH key grant

**Status:** VERIFIED
**Location:** `internal/api/api.go:663-704` (`handleSSHConfig`), `cmd/ternalctl/main.go:515-576` (`cmdProxy`)

The `/ssh-config` export generates proxy commands but never calls `IssueSSHAccess`. The `cmdProxy` path creates relay grants but not SSH key grants.

### TERN-B266-013: `ternalctl ssh` races agent key installation

**Status:** VERIFIED
**Location:** `internal/api/api.go:571-661` (`handleIssueSSHCommand`), `cmd/ternalctl/main.go:346-409` (`cmdSSH`)

The API records the grant and returns an SSH command immediately. The agent installs the key only on its next synchronization cycle (default 60 seconds).

### TERN-B266-014: Relay/discovery authorization loses requested SSH account

**Status:** VERIFIED
**Location:** `internal/api/api.go:706-772` (`handleIssueRelayGrant`)

`handleIssueRelayGrant` checks `host.SSHUser` (line 752), not the user's requested SSH account. The SSH access endpoint (`handleIssueSSHCommand`) checks `req.SSHUser` correctly.

### TERN-B266-015: Persisted CLI credentials not bound to API origin

**Status:** VERIFIED
**Location:** `cmd/ternalctl/main.go:26-30` (`Session` struct), `main.go:764-784` (`loadSession`)

The `Session` struct contains `Cookie`, `CSRFToken`, and `ExpiresAt` but no API origin. `loadSession` reads from a fixed path regardless of `TERNAL_API_URL`.

---

## P2 — Cancellation, Operation, and Deployment

### TERN-B266-016: Unbounded endpoint-ID subprocess can stall supervision

**Status:** VERIFIED
**Location:** `cmd/ternal-agent/main.go:441-451` (`roostEndpointID`)

`exec.Command(cfg.Pigeons, "endpoint-id").Output()` has no context or deadline. Called during `loadDevice` (line 428) which is invoked by `supervise`, `heartbeat`, and `syncAuthorizedKeys`.

### TERN-B266-017: Store/trust locks defeat request cancellation budgets

**Status:** VERIFIED
**Location:** `internal/store/store.go:35` (`sync.RWMutex`), `internal/store/rhiza_sql.go:23` (`trustMu`)

`store.mu` is acquired via `Lock()`/`RLock()` without context awareness. `rhiza_sql.go:176` does use `context.WithoutCancel` for the fenced write, but the initial `trustMu.Lock()` at line 176 is not cancellable.

### TERN-B266-018: Public request bodies have no read deadline

**Status:** VERIFIED
**Location:** `cmd/ternal-api/main.go:40-45`

`ReadHeaderTimeout` and `IdleTimeout` are set, but no `ReadTimeout` or body-read deadline exists. The 1 MiB body limit (`api.go:127`) does not limit transmission time.

### TERN-B266-019: Device key generation silently replaces enrolled identity's key

**Status:** VERIFIED
**Location:** `internal/deviceauth/deviceauth.go:27-39` (`GenerateKey`)

`GenerateKey` calls `writePrivate` which overwrites any existing file without checking.

### TERN-B266-020: Growing histories are repeatedly scanned and materialized

**Status:** VERIFIED
**Location:** `internal/store/store.go:1044-1061` (`ListAccessGrants`), `store.go:1063-1080` (`ListAccessRequests`)

Both functions read all rows with no WHERE clause and filter in application memory. No pagination or index on `created_at` exists.

### TERN-B266-021: Secret-only Helm updates don't update running credentials

**Status:** VERIFIED
**Location:** `deploy/helm/ternal/templates/statefulset.yaml:64`

The pod-template annotation includes `checksum/config` (configmap hash) but not a secret checksum. Kubernetes does not refresh env-var secrets in running containers.

### TERN-B266-022: Release tags published before all release gates succeed

**Status:** VERIFIED
**Location:** `.github/workflows/release.yml:102-118`

The `release` job pushes the image tag to GHCR at line 102 (before scanning at line 120 and signing at line 142). The `publish` job at line 168 does wait for both `native` and `release` jobs, but the image tag is already available.

### TERN-B266-023: Windows bundle lookup doesn't find bundled executable

**Status:** VERIFIED
**Location:** `cmd/ternal-agent/main.go:581-601` (`findPigeons`), `cmd/ternalctl/main.go:655-677` (`findPigeons`)

Both `findPigeons` functions look for `pigeons` (no `.exe` suffix) via `os.Stat`. On Windows, the bundled executable is `pigeons.exe`.

### TERN-B266-024: Trustguard resource names collide for distinct cluster IDs

**Status:** VERIFIED
**Location:** `deploy/security/render_trustguard.py:181`

`suffix = cluster_id[-35:]` — distinct cluster IDs sharing their last 35 characters produce identical resource names.

### TERN-B266-025: Recursive SSH execution requires bare `ternalctl` on PATH

**Status:** VERIFIED
**Location:** `internal/core/core.go:274`

`buildGrantAwareProxyCommand` generates `ternalctl proxy %h:%p` with bare `ternalctl`. The `cmdSSH` function at `ternalctl/main.go:392-396` replaces `KnownHostsCommand=ternalctl` with the absolute path but leaves `ProxyCommand=ternalctl` unchanged.

### TERN-B266-026: Timestamp freshness arithmetic can overflow

**Status:** VERIFIED
**Location:** `internal/deviceauth/deviceauth.go:73-79` (`Fresh`)

```go
delta := now.Unix() - timestamp
if delta < 0 {
    delta = -delta
}
```

At `math.MinInt64`, `-delta` still yields a negative value, allowing an extreme timestamp to pass the freshness check.

### TERN-B266-027: Backup archives inherit potentially public permissions

**Status:** VERIFIED
**Location:** `deploy/backup/local-backup.sh`

No `umask` or explicit permission enforcement. `mkdir -p "$out"` and `tar -czf` inherit the caller's umask.

### TERN-B266-028: Local transport fixture published on all host interfaces

**Status:** VERIFIED
**Location:** `deploy/pigeons-smoke/drivers/linux-netns-pigeons.sh:171`

`docker run -d --name "$relay_name" -p "0.0.0.0:$relay_port:3340"` binds to all interfaces.

---

## P2 — Design Boundaries (require explicit triage)

### TERN-B266-D01: Unknown pending trust writes stop recovery

**Status:** VERIFIED
**Location:** `internal/store/store.go:370-393` (`recoverPendingTrust`)

If the trust anchor has an unresolved pending write whose request status is neither committed nor rejected, the function returns an error, refusing service. This is the correct safety default but has no automatic recovery.

### TERN-B266-D02: Policy/session revocation and grants have different lifetimes

**Status:** VERIFIED
**Location:** `internal/api/api.go:656` (`IssueSSHAccess` creates 5-minute grant), `store.go:679-689` (`DeleteHost` doesn't revoke grants)

Existing grants are not invalidated by policy deletion. The 5-minute SSH grant TTL is independent of session/policy lifetime.

### TERN-B266-D03: Trustguard doesn't isolate from arbitrary workload creators

**Status:** VERIFIED
**Location:** `deploy/security/render_trustguard.py:82-88` (`api_container_expr`)

The admission policy identifies API containers by name or official image reference. Whether another workload can obtain the protected service account depends on external RBAC.

### TERN-B266-D04: Filesystem backup is only a cold-backup procedure

**Status:** VERIFIED
**Location:** `deploy/backup/local-backup.sh`, `deploy/backup/local-backup-restore.test.sh`

The restore test explicitly stops the server before archiving. No consistent snapshot or application backup barrier is used.

### TERN-B266-D05: Audit coverage not uniform across authorization-material changes

**Status:** VERIFIED
**Location:** `internal/store/store.go:761-773` (`CreateSSHKey` — no audit), `store.go:923-936` (`DeleteSSHKeyForUser` — audit only for admin), `store.go:1352-1441` (`EnrollDevice` — no audit)

Self-service SSH key creation/deletion and enrollment don't produce audit events.

---

## P3 — Lower Priority

### TERN-B266-H01 to H03: Performance Hypotheses

These are measurement tasks, not code bugs. The code paths described (trust fencing overhead, OIDC discovery caching, unnecessary polling work) exist as described.

---

## Design Decisions (2026-09-19)

| ID | Decision | Status |
|----|----------|--------|
| D01 | 자동 복구로 결정. Rhiza에 terminal-resolution 기능 요청 ([rhiza#136](https://github.com/mrchypark/rhiza/issues/136)). 기능 제공 전까지는 fail-closed 유지 + 운영자 복구 runbook 추가 | Rhiza 의존 |
| D02 | Option C — 레이어별 SLA 명시. 신규 결정 즉시, outstanding grant는 5분 TTL 상한, 설치 키는 `expiry-time` 강제(#73와 함께), 확립 세션은 강제 종료 안 함. 기기 폐기 시 grant 즉시 만료는 유지/확장 | 문서화 + #73 |
| D03 | Option A — 현 스코프("실수 방지")를 문서에 명시. 적대적 격리는 별도 이슈로 분리 | 문서화 |
| D04 | Option A — cold-backup 전제 강제 (서버 실행 중 백업 거부). 엔진 스냅샷(B)은 로드맵 | 스크립트 수정 |
| D05 | Option B — 인증 자료 변경 전체를 트랜잭션 감사. 단 #90(페이지네이션·보존 정책) 선행 | #90 선행 |

## Issues to Create

Based on the verification, the following GitHub issues should be created:

| Issue | Priority | Title |
|-------|----------|-------|
| #1 | P1 | `validDirectAddress` accepts shell-metacharacter zone IDs; ProxyCommand is shell-interpreted |
| #2 | P1 | SSH config `Host` directive uses raw host name; enrollment serial has no control-char validation |
| #3 | P1 | Installed authorized-key lines have no expiry; grants outlive agent/API failures |
| #4 | P2 | Authorized keys snapshot read and generation publication are not atomic |
| #5 | P2 | No aggregate snapshot size budget; agent truncates at 1 MiB |
| #6 | P2 | Snapshot SQL produces K×G cross product before Go deduplication |
| #7 | P2 | Sync lock (directory) survives process crash; requires manual removal |
| #8 | `0600` without ownership assignment breaks non-root SSH users when agent runs as root |
| #9 | `atomicWrite` does not fsync parent directory before rename |
| #10 | Enrollment response loss leaves consumed token without recoverable identity |
| #11 | `DeleteHost` doesn't cascade to devices; `GetDeviceBySerial` is ambiguous |
| #12 | SSH config export path never creates SSH key grant |
| #13 | `ternalctl ssh` races agent key installation (default 60s poll) |
| #14 | Relay grant checks `host.SSHUser` instead of requested SSH account |
| #15 | Persisted CLI session not bound to API origin |
| #16 | `roostEndpointID` has no context/deadline; can stall supervisor |
| #17 | `store.mu` and `trustMu` acquired without cancellable wait |
| #18 | HTTP servers have no request-body read deadline |
| #19 | `GenerateKey` silently overwrites existing device key |
| #20 | `ListAccessGrants`/`ListAccessRequests` scan all rows without pagination |
| #21 | Helm secret changes don't trigger pod restart |
| #22 | Release workflow pushes image tag before all gates pass |
| #23 | Windows `findPigeons` looks for `pigeons` not `pigeons.exe` |
| #24 | Trustguard resource names collide on shared cluster_id suffix |
| #25 | ProxyCommand uses bare `ternalctl` (not absolute path) |
| #26 | `Fresh()` timestamp comparison can overflow at int64 minimum |
| #27 | Backup script doesn't enforce restrictive umask/permissions |
| #28 | Local relay fixture binds to `0.0.0.0` |
