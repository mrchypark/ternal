# Verification

## Deterministic repository checks

Run `./scripts/run-all-tests.sh`. It verifies:

- generated htmx/Tailwind assets and a clean asset diff;
- Go formatting, race-enabled tests, vet, and all three binaries;
- data-store reopen persistence and offline archive readback (not trust-state reactivation);
- gomponents rendering, escaping, htmx fragments, and embedded asset hash;
- OIDC state/session integrity and old-origin rejection;
- persistent logout revocation and captured-cookie replay rejection;
- strict host-key acceptance/rejection and endpoint-ID-only route rejection;
- 300-second relay grant admission and ungranted denial;
- signed device requests, batch enrollment, authorized-key monotonic generation,
  exact current-snapshot acknowledgement, and agent rollback/equivocation state;
- live-scenario entrypoint syntax, executability, and parent orchestration;
- pinned pigeons source/patch checks, packaging, and transport result parsing;
- Helm lint/render checks when Helm is installed.

## Verification that remains environment-bound

These checks require an isolated provider, relay, SSH server, and cluster. They
must be run against artifacts built from the exact reviewed commit:

- authorization-code login and CLI device flow;
- wrong issuer/audience/nonce/state/refresh/device token rejection;
- absence of runtime traffic to the legacy provider;
- managed and direct pigeons routes;
- relay callback bearer plus exact endpoint subject;
- strict wrong-key rejection and correct-key SSH banner/session;
- bidirectional stream drain, half-close, and process exit semantics;
- zero-replica cutover, workload health, and fail-closed rollback.

Passing repository tests is not evidence of those live behaviors. Roost startup
alone is not SSH compatibility evidence.

The Go greenfield release deliberately rejects restoring an older trust
database. The archive readback test proves storage recovery only; it does not
prove that revocations, grants, or authorized-key generations remain monotonic
after rollback. A future live restore path requires a separately reviewed
security-epoch protocol and adversarial post-restore tests.
