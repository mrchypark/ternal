package store

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrchypark/rhiza/pkg/quepaxa"
	"github.com/mrchypark/rhiza/pkg/recovery"
	"github.com/mrchypark/rhiza/pkg/recoveryanchor"
	"github.com/thanos-io/objstore"
)

type anchorVerifier func(context.Context, recoveryanchor.Request, recoveryanchor.Record) (recoveryanchor.Binding, []byte, error)

// anchorServiceFixture serves the real handler over TLS with the operator's
// real client. No Ternal voter runs anywhere: the operator activates the anchor
// while the recovered StatefulSet is still unready, so the service has to work
// on its own.
func anchorServiceFixture(t *testing.T, record trustAnchorRecord, verify anchorVerifier) (*recoveryanchor.Client, *countingAnchor) {
	t.Helper()
	backend := &countingAnchor{record: record, rv: 1}
	coordinator := &recoveryanchor.Coordinator{
		Backend:        &recoveryAnchorBackend{anchor: backend, id: "ternal-anchor"},
		EvidenceFormat: trustAnchorEvidenceFormat,
		Verifier:       verify,
	}
	tokenPath := t.TempDir() + "/token"
	if err := os.WriteFile(tokenPath, []byte("anchor-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The base context no request can cancel is the whole point: the service
	// owns the activation lifetime, not the caller's connection.
	server := httptest.NewTLSServer(NewAnchorHandler(coordinator, "anchor-token", context.Background(), AnchorActivationBudget))
	t.Cleanup(server.Close)
	return &recoveryanchor.Client{URL: server.URL, TokenFile: tokenPath, Client: server.Client()}, backend
}

func fixtureVerifier(t *testing.T, bucket objstore.Bucket, storage recoveryStorage) anchorVerifier {
	t.Helper()
	return recoveryVerifier{bucket: bucket, storage: storage}.verify
}

// TestAnchorServiceServesTheRecoveryProtocol is the packaged-service check:
// the operator's client talks to the real handler, a target that carries the
// frozen pair commits, and a wrong token or an untrusted certificate does not.
func TestAnchorServiceServesTheRecoveryProtocol(t *testing.T) {
	bucket, storage, request, record := recoveryFixture(t)
	client, backend := anchorServiceFixture(t, record, fixtureVerifier(t, bucket, storage))

	receipt, err := client.Activate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	committed, _ := backend.snapshot()
	if receipt.Generation != 1 || committed.ClusterID != request.TargetClusterID {
		t.Fatalf("receipt = %#v, committed = %#v", receipt, committed)
	}
	// The operator re-verifies the receipt it holds before it starts the target.
	if err := client.Verify(context.Background(), request, receipt); err != nil {
		t.Fatal(err)
	}

	wrongToken := t.TempDir() + "/token"
	if err := os.WriteFile(wrongToken, []byte("not-the-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rejected := &recoveryanchor.Client{URL: client.URL, TokenFile: wrongToken, Client: client.Client}
	if _, err := rejected.Activate(context.Background(), request); err == nil {
		t.Fatal("a wrong bearer token was accepted")
	}
	if err := rejected.Verify(context.Background(), request, receipt); err == nil {
		t.Fatal("a wrong bearer token verified a receipt")
	}
	emptyToken := t.TempDir() + "/token"
	if err := os.WriteFile(emptyToken, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&recoveryanchor.Client{URL: client.URL, TokenFile: emptyToken, Client: client.Client}).Activate(context.Background(), request); err == nil {
		t.Fatal("an empty token file was accepted")
	}
	// The upstream client reads the token file before it dials, so the wrong
	// token proves the bearer check; a client with no pinned CA proves that the
	// self-signed server certificate alone is not trusted.
	untrusted := &recoveryanchor.Client{URL: client.URL, TokenFile: wrongToken}
	if _, err := untrusted.Activate(context.Background(), request); err == nil {
		t.Fatal("an untrusted server certificate was accepted")
	}
}

// TestAnchorServiceOutlivesTheClientTimeout is the liveness contract. The
// pinned client clamps its timeout, and the upstream handler passes the request
// context into the coordinator, so a proof can outlive the request that asked
// for it. The service keeps the activation alive on its own budget, and the
// retry returns the receipt of the single generation it committed instead of
// proving the archive a second time.
func TestAnchorServiceOutlivesTheClientTimeout(t *testing.T) {
	bucket, storage, request, record := recoveryFixture(t)
	inner := fixtureVerifier(t, bucket, storage)
	release := make(chan struct{})
	var proofs int32
	verify := func(ctx context.Context, req recoveryanchor.Request, rec recoveryanchor.Record) (recoveryanchor.Binding, []byte, error) {
		atomic.AddInt32(&proofs, 1)
		select {
		case <-release:
		case <-ctx.Done():
			return recoveryanchor.Binding{}, nil, ctx.Err()
		}
		return inner(ctx, req, rec)
	}
	client, backend := anchorServiceFixture(t, record, verify)

	// Well under the client's ten-second clamp, so the check does not have to
	// wait the clamp out.
	impatient := &recoveryanchor.Client{URL: client.URL, TokenFile: client.TokenFile, Client: &http.Client{Timeout: 250 * time.Millisecond, Transport: client.Client.Transport}}
	if _, err := impatient.Activate(context.Background(), request); err == nil {
		t.Fatal("the impatient client reported success before the proof finished")
	}
	if got := atomic.LoadInt32(&proofs); got != 1 {
		t.Fatalf("verifier ran %d times before the retry", got)
	}
	committed, _ := backend.snapshot()
	if committed.Receipt != nil {
		t.Fatal("the timed-out request acknowledged success before the commit")
	}

	// The abandoned activation is still running on the service's own budget.
	close(release)
	deadline := time.Now().Add(30 * time.Second)
	for {
		committed, _ = backend.snapshot()
		if committed.Receipt != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if committed.Receipt == nil {
		t.Fatal("the abandoned activation never committed")
	}
	receipt := *committed.Receipt

	retried, err := client.Activate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if retried != receipt {
		t.Fatalf("retry returned %#v, committed %#v", retried, receipt)
	}
	if got := atomic.LoadInt32(&proofs); got != 1 {
		t.Fatalf("the retry proved the archive again: %d verifier runs", got)
	}
	final, rv := backend.snapshot()
	// One reserve plus one commit, and the retry that found the committed
	// receipt wrote nothing.
	if final.Generation != 1 || rv != 3 {
		t.Fatalf("generation = %d after %d writes, want one committed generation", final.Generation, rv-1)
	}
}

// TestAnchorServiceRefusesAnUnprovenTarget keeps the refusal path honest: a
// target that does not carry the frozen pair must not commit, and the frozen
// transition stays where a corrected retry can settle it.
func TestAnchorServiceRefusesAnUnprovenTarget(t *testing.T) {
	bucket, storage, request, record := recoveryFixture(t)
	client, backend := anchorServiceFixture(t, record, fixtureVerifier(t, bucket, storage))

	unproven := request
	unproven.TargetMembership = recovery.NewMembershipRecord(request.TargetClusterID, []quepaxa.Member{{ID: "other", Token: "other"}}, "async")
	if _, err := client.Activate(context.Background(), unproven); err == nil {
		t.Fatal("an unproven target was committed")
	}
	refused, _ := backend.snapshot()
	// The refusal leaves the frozen transition exactly where it was, still
	// bound to the source, with no receipt claiming the recovery happened.
	if refused.ClusterID != request.SourceClusterID || refused.Receipt != nil || refused.Transition == nil || refused.Generation != 0 {
		t.Fatalf("refusal changed the anchor: %#v", refused)
	}
	// And the refusal repeats rather than succeeding on a second attempt: a
	// target that cannot prove the frozen pair never becomes ready.
	if _, err := client.Activate(context.Background(), unproven); err == nil {
		t.Fatal("the same unproven target was committed on a retry")
	}
}
