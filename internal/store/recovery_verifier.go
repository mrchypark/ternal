package store

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path"

	kitlog "github.com/go-kit/log"
	"github.com/mrchypark/rhiza/pkg/recovery"
	"github.com/mrchypark/rhiza/pkg/recoveryanchor"
	"github.com/thanos-io/objstore"
	"github.com/thanos-io/objstore/providers/filesystem"
	"github.com/thanos-io/objstore/providers/s3"
)

// recoveryStorage is the object-store recipe the anchor service verifies
// against.  It is read from the same TERNAL_* variables the voters use, so the
// service cannot verify a different root than the generation it recovers.
type recoveryStorage struct {
	Provider, Endpoint, Bucket, Prefix, Region, Dir string
	Insecure                                        bool
	AccessKey, SecretKey, SessionToken              string
}

func recoveryStorageFromEnv() recoveryStorage {
	return recoveryStorage{
		Provider: os.Getenv("TERNAL_OBJECT_STORE_PROVIDER"), Endpoint: os.Getenv("TERNAL_OBJECT_STORE_ENDPOINT"),
		Bucket: os.Getenv("TERNAL_OBJECT_STORE_BUCKET"), Prefix: os.Getenv("TERNAL_OBJECT_STORE_PREFIX"),
		Region: os.Getenv("TERNAL_OBJECT_STORE_REGION"), Dir: os.Getenv("TERNAL_OBJECT_STORE_DIR"),
		Insecure:  os.Getenv("TERNAL_OBJECT_STORE_INSECURE") == "true",
		AccessKey: os.Getenv("TERNAL_OBJECT_STORE_ACCESS_KEY"), SecretKey: os.Getenv("TERNAL_OBJECT_STORE_SECRET_KEY"),
		SessionToken: os.Getenv("TERNAL_OBJECT_STORE_SESSION_TOKEN"),
	}
}

// identity and prefix mirror the storage binding Ternal writes, so a recovery
// request is checked against configuration rather than against itself.
func (c recoveryStorage) identity(clusterID string) string {
	return TrustAnchorStorageID(c.Provider, c.Endpoint, c.Bucket, c.Prefix, clusterID)
}

func (c recoveryStorage) prefix(clusterID string) string { return path.Join(c.Prefix, clusterID) }

// bucket builds the bucket the recovered-evidence helper reads and pins
// through.  An unsupported or incomplete provider is an error: silently
// falling back to local storage would verify a different root than the voters
// run on.
//
// ponytail: s3 and filesystem only, the two paths this integration is deployed
// and tested with; add gcs/azure constructors when recovery runs on them.
func (c recoveryStorage) bucket() (objstore.Bucket, error) {
	switch c.Provider {
	case "s3":
		if c.Bucket == "" {
			return nil, fmt.Errorf("TERNAL_OBJECT_STORE_BUCKET is required for an s3 recovery store")
		}
		return s3.NewBucketWithConfig(kitlog.NewNopLogger(), s3.Config{
			Bucket: c.Bucket, Endpoint: c.Endpoint, Region: c.Region, Insecure: c.Insecure,
			AWSSDKAuth: c.AccessKey == "", AccessKey: c.AccessKey, SecretKey: c.SecretKey, SessionToken: c.SessionToken,
		}, "ternal-anchor", nil)
	case "filesystem":
		dir := c.Dir
		if dir == "" {
			dir = "./objstore"
		}
		return filesystem.NewBucket(dir)
	default:
		return nil, fmt.Errorf("unsupported recovery object store provider %q", c.Provider)
	}
}

// recoveryVerifier proves that the recovered target generation actually carries
// the application trust pair the transition froze.  The proof is rebuilt from
// the recovered database rather than echoed from the request, so a stale or
// unrelated archive produces different bytes and the coordinator refuses it.
type recoveryVerifier struct {
	bucket  objstore.Bucket
	storage recoveryStorage
}

func (v recoveryVerifier) verify(ctx context.Context, req recoveryanchor.Request, rec recoveryanchor.Record) (recoveryanchor.Binding, []byte, error) {
	if rec.PendingWrite {
		return recoveryanchor.Binding{}, nil, fmt.Errorf("anchor still has a pending application write")
	}
	if rec.Version != recoveryanchor.Version1 || rec.EvidenceFormat != trustAnchorEvidenceFormat {
		return recoveryanchor.Binding{}, nil, fmt.Errorf("unsupported anchor record")
	}
	frozen, err := parseTrustAnchorEvidence(rec.Evidence)
	if err != nil {
		return recoveryanchor.Binding{}, nil, err
	}
	if frozen.PendingEpoch != nil || frozen.PendingID != "" {
		return recoveryanchor.Binding{}, nil, fmt.Errorf("anchor still has a pending application write")
	}
	if frozen.Bootstrap {
		return recoveryanchor.Binding{}, nil, fmt.Errorf("anchor bootstrap was never consumed")
	}
	var anchorHash [32]byte
	decoded, err := hex.DecodeString(req.TargetAnchorHash)
	if err != nil || len(decoded) != len(anchorHash) {
		return recoveryanchor.Binding{}, nil, fmt.Errorf("target anchor hash must be 32-byte hex")
	}
	copy(anchorHash[:], decoded)
	if rec.Binding.ClusterID != req.SourceClusterID {
		return recoveryanchor.Binding{}, nil, fmt.Errorf("anchor binds cluster %q, request source %q", rec.Binding.ClusterID, req.SourceClusterID)
	}
	if rec.Binding.StorageID != v.storage.identity(req.SourceClusterID) {
		return recoveryanchor.Binding{}, nil, fmt.Errorf("anchor storage identity does not match the configured source storage")
	}
	if req.SourcePrefix != v.storage.prefix(req.SourceClusterID) || req.TargetPrefix != v.storage.prefix(req.TargetClusterID) {
		return recoveryanchor.Binding{}, nil, fmt.Errorf("recovery prefixes do not match the configured object-store root")
	}
	var proof []byte
	if _, err := recovery.RecoverApplicationEvidence(ctx, v.bucket, req.TargetPrefix, recovery.EvidenceOptions{
		ExpectedAnchorHash:   anchorHash,
		ExpectedMembership:   req.TargetMembership,
		ExpectedForkResult:   req.Fork,
		ExpectedSourcePrefix: req.SourcePrefix,
		ExpectedOperationID:  req.OperationID,
	}, func(ctx context.Context, materialized *recovery.RecoveredMaterializer) error {
		rows, err := materialized.Query(ctx, "SELECT epoch, token FROM trust_state WHERE id = 1")
		if err != nil {
			return fmt.Errorf("query recovered trust state: %w", err)
		}
		defer rows.Close()
		if !rows.Next() {
			return fmt.Errorf("recovered trust state is empty")
		}
		var epoch int64
		var token string
		if err := rows.Scan(&epoch, &token); err != nil {
			return fmt.Errorf("scan recovered trust state: %w", err)
		}
		if rows.Next() {
			return fmt.Errorf("recovered trust state has more than one row")
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if epoch < 1 || !validUUID(token) {
			return fmt.Errorf("recovered trust state is not a settled application pair")
		}
		// Rebuilt from the recovered pair, never echoed from the frozen bytes:
		// a target that does not carry the frozen pair yields different proof
		// and cannot be committed.
		proof = trustAnchorEvidence{Format: frozen.Format, Epoch: epoch, Token: token}.bytes()
		return nil
	}); err != nil {
		return recoveryanchor.Binding{}, nil, err
	}
	if proof == nil {
		return recoveryanchor.Binding{}, nil, fmt.Errorf("recovery produced no evidence")
	}
	return recoveryanchor.Binding{ClusterID: req.TargetClusterID, StorageID: v.storage.identity(req.TargetClusterID)}, proof, nil
}
