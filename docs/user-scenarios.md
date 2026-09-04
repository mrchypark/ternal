# User scenarios

| Scenario | Expected behavior | Repository evidence | Live evidence required |
|---|---|---|---|
| Admin signs in | Exact configured OIDC issuer and group produce a bounded local session | auth/API tests | authorization-code flow |
| CLI signs in | Device flow returns a mode-0600 local session without persisting provider tokens | auth/CLI tests | `local-cli-scenario.sh` with provider device flow |
| Admin enrolls a batch | One token allocates deterministic serials up to its limit, then closes | store tests | disposable device |
| Device authenticates | Ed25519 signature, timestamp, endpoint, host key, and serial must all match | device/API tests | agent heartbeat |
| User registers a key | Valid OpenSSH key is canonicalized and scoped to its owner | API/store tests | portal/CLI request |
| User requests SSH | Policy, key, verified host fingerprint, and explicit route are mandatory; host lists do not disclose transport endpoint IDs | core/API/CLI tests | `local-agent-scenario.sh` plus managed/direct connection |
| Client joins relay | A policy grant lasts exactly 300 seconds and binds one client endpoint | relay tests | real callback capture |
| Wrong host key appears | SSH stops; no permissive known-host fallback exists | core/CLI tests | real wrong-key server |
| Agent syncs keys | The signed ACK must match the current content digest and generation; changed, stale, rollback, and equivocation state is rejected | store/API/agent tests | repeated `local-agent-scenario.sh` sync |
| Device is revoked | SSH and relay grants, discovery, and callback admission fail; the agent stops transport and trust is not recreated | store/API/agent tests | live revoked device |
| Portal is used | HTML works server-side; htmx fragments enhance navigation; untrusted text is escaped | web tests/local smoke | browser accessibility pass |
| Server restarts | A fresh emptyDir cache rebuilds from certified object-store state | persistence/backup tests | pod recreation with no PVC |
| Old provider is presented | Old issuer and endpoint origin are rejected with no fallback | auth tests | negative provider probes |
| User signs out | The browser cookie is cleared, its captured signed value is rejected, and revocation survives API restart | auth/API/store tests | `rauthy-session-scenario.sh` replay probe plus workload restart |
