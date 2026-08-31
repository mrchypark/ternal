# User scenarios

| Scenario | Expected behavior | Repository evidence | Live evidence required |
|---|---|---|---|
| Admin signs in | Exact configured OIDC issuer and group produce a bounded local session | auth/API tests | authorization-code flow |
| CLI signs in | Device flow returns a local session without persisting provider tokens | auth/CLI tests | Rauthy device flow |
| Admin enrolls a batch | One token allocates deterministic serials up to its limit, then closes | store tests | disposable device |
| Device authenticates | Ed25519 signature, timestamp, endpoint, host key, and serial must all match | device/API tests | agent heartbeat |
| User registers a key | Valid OpenSSH key is canonicalized and scoped to its owner | API/store tests | portal/CLI request |
| User requests SSH | Policy, key, verified host fingerprint, and explicit route are mandatory | core/API tests | managed/direct connection |
| Client joins relay | A policy grant lasts exactly 300 seconds and binds one client endpoint | relay tests | real callback capture |
| Wrong host key appears | SSH stops; no permissive known-host fallback exists | core/CLI tests | real wrong-key server |
| Agent syncs keys | Content digest is checked, generations increase, rollback/equivocation is rejected | store/agent tests | repeated running sync |
| Device is revoked | Signed heartbeat and key synchronization fail; host is not silently recreated | store/API tests | live revoked device |
| Portal is used | HTML works server-side; htmx fragments enhance navigation; untrusted text is escaped | web tests/local smoke | browser accessibility pass |
| Server restarts | Rhiza state remains on the configured volume | persistence/backup tests | container/PVC restart |
| Old provider is presented | Old issuer and endpoint origin are rejected with no fallback | auth tests | negative provider probes |
