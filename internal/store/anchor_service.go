package store

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mrchypark/rhiza/pkg/recoveryanchor"
	"github.com/thanos-io/objstore"
)

// AnchorActivationBudget is the service-owned lifetime of one activation.
//
// The pinned client clamps its own timeout to ten seconds and the upstream
// handler passes the request context into the coordinator, so a proof that
// outlives a single request would be cancelled and restarted forever. The
// service therefore runs activation under its own budget: a client that gives
// up is answered by retrying the same operation, which returns the committed
// receipt instead of proving the archive again.
const AnchorActivationBudget = 2 * time.Minute

// NewAnchorCoordinator composes the record adapter, the recovered-evidence
// verifier, and the configured object-store root into the coordinator the
// anchor service serves.  It is the whole assembly: the service owns no state
// of its own, so a restart cannot lose a transition.
func NewAnchorCoordinator(anchorID string, backend recoveryanchor.Backend, bucket objstore.Bucket, storage recoveryStorage) (*recoveryanchor.Coordinator, error) {
	if anchorID == "" || backend == nil || bucket == nil {
		return nil, fmt.Errorf("anchor service requires an id, a record backend, and a bucket")
	}
	return &recoveryanchor.Coordinator{
		Backend:        backend,
		EvidenceFormat: trustAnchorEvidenceFormat,
		Verifier:       recoveryVerifier{bucket: bucket, storage: storage}.verify,
	}, nil
}

// NewAnchorHandler serves the recovery protocol under a service-owned
// activation lifetime.  It never acknowledges success before the commit: the
// response is written from the coordinator's result, and a response lost with
// the client's connection is recovered by retrying the same operation.
func NewAnchorHandler(coord *recoveryanchor.Coordinator, token string, base context.Context, budget time.Duration) http.Handler {
	handler := recoveryanchor.NewHandler(coord, token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(base, budget)
		defer cancel()
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

// OpenAnchorService assembles the anchor service from the deployment's
// environment.  It deliberately shares the voters' object-store variables: a
// service that verified a different root would not be an authority for the
// generation it recovers.
func OpenAnchorService(base context.Context) (http.Handler, error) {
	anchorID := os.Getenv("TERNAL_RECOVERY_ANCHOR_ID")
	if anchorID == "" {
		return nil, fmt.Errorf("TERNAL_RECOVERY_ANCHOR_ID is required")
	}
	name := os.Getenv("TERNAL_TRUST_ANCHOR_CONFIGMAP")
	if name == "" {
		return nil, fmt.Errorf("TERNAL_TRUST_ANCHOR_CONFIGMAP is required")
	}
	// The namespace is never read from a service-account file here: the anchor
	// Deployment runs in its own namespace and must name the ConfigMap's
	// namespace explicitly rather than inherit whatever pod it happens to be.
	namespace := os.Getenv("TERNAL_TRUST_ANCHOR_NAMESPACE")
	if namespace == "" {
		return nil, fmt.Errorf("TERNAL_TRUST_ANCHOR_NAMESPACE is required")
	}
	token, err := anchorBearerToken(os.Getenv("TERNAL_ANCHOR_TOKEN_FILE"))
	if err != nil {
		return nil, err
	}
	storage := recoveryStorageFromEnv()
	bucket, err := storage.bucket()
	if err != nil {
		return nil, err
	}
	backend, err := NewRecoveryAnchorBackend(anchorID, name, namespace)
	if err != nil {
		return nil, err
	}
	coord, err := NewAnchorCoordinator(anchorID, backend, bucket, storage)
	if err != nil {
		return nil, err
	}
	return NewAnchorHandler(coord, token, base, AnchorActivationBudget), nil
}

// anchorBearerToken reads the operator's bearer token.  An empty or missing
// token is a startup error rather than a service that denies every request:
// the upstream handler treats an empty token as "deny all", which would look
// like a healthy anchor that never activates anything.
func anchorBearerToken(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("TERNAL_ANCHOR_TOKEN_FILE is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read anchor token: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("anchor token file %s is empty", path)
	}
	return token, nil
}
