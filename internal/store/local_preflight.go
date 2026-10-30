package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/mrchypark/rhiza"
	"github.com/mrchypark/rhiza/pkg/checkpoint"
	"github.com/mrchypark/rhiza/pkg/quepaxa"
	"github.com/mrchypark/rhiza/pkg/recovery"
	"github.com/thanos-io/objstore"
)

const localModeMarker = ".ternal-data-mode.json"
const maxLocalReceipt = 128 << 10

// LocalRestoreFromEnv verifies authority, restores through the SDK, and closes
// before main reopens the same DataDir. It starts no application SQL or services.
func LocalRestoreFromEnv(ctx context.Context) error {
	if err := LocalPreflightFromEnv(ctx); err != nil {
		return err
	}
	c, err := rhizaConfigFromEnv(envOr("TERNAL_DATA_DIR", "ternal-data"))
	if err != nil {
		return err
	}
	if err := prepareLocalOpen(ctx, c); err != nil {
		return err
	}
	db, err := rhiza.Open(ctx, c)
	if err != nil {
		return fmt.Errorf("open Local init recovery: %w", err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close Local init recovery: %w", err)
	}
	return nil
}

// readOnlyLocalBucket denies writes even if an SDK read path changes later.
type readOnlyLocalBucket struct{ objstore.Bucket }

func (readOnlyLocalBucket) Upload(context.Context, string, io.Reader, ...objstore.ObjectUploadOption) error {
	return errors.New("Local preflight forbids object-store writes")
}
func (readOnlyLocalBucket) Delete(context.Context, string) error {
	return errors.New("Local preflight forbids object-store deletes")
}

type localArchiveView struct {
	Binding    recovery.LocalBinding   `json:"binding"`
	Tip        quepaxa.Slot            `json:"tip"`
	PrefixHash [32]byte                `json:"prefix_hash"`
	Snapshot   *recovery.LocalSnapshot `json:"snapshot"`
}

type localPreflightReceipt struct {
	Version         int              `json:"version"`
	StorageID       string           `json:"storage_id"`
	NodeID          string           `json:"node_id"`
	Scope           string           `json:"scope"`
	AnchorName      string           `json:"anchor_name"`
	AnchorNamespace string           `json:"anchor_namespace"`
	View            localArchiveView `json:"view"`
}

type localDataMode struct {
	Version   int                   `json:"version"`
	Mode      string                `json:"mode"`
	StorageID string                `json:"storage_id"`
	Binding   recovery.LocalBinding `json:"binding"`
}

func localStorageID(c rhiza.Config) string {
	if c.ObjStoreProvider == "azure" {
		return localStorageDomain(AzureTrustAnchorStorageID(c.ObjStoreAzureStorageAccount, azureEndpointFromRhiza(c), c.ObjStoreBucket, path.Join(c.ObjStorePrefix, c.ClusterID, "local/v1"), c.ClusterID))
	}
	return LocalTrustAnchorStorageID(c.ObjStoreProvider, c.ObjStoreEndpoint, c.ObjStoreBucket, c.ObjStorePrefix, c.ClusterID)
}

func localNamespace(c rhiza.Config) string {
	return path.Join(c.ObjStorePrefix, c.ClusterID, "local/v1")
}

func readLocalArchive(ctx context.Context, bucket objstore.Bucket, c rhiza.Config) (*recovery.LocalArchive, localArchiveView, error) {
	a, err := recovery.OpenLocalArchive(ctx, readOnlyLocalBucket{bucket}, localNamespace(c))
	if err != nil {
		return nil, localArchiveView{}, fmt.Errorf("read seeded Local archive (manual SDK seeding is required; no automatic bootstrap): %w", err)
	}
	a = a.ReadOnly()
	if err := validateLocalBinding(c, a.Binding); err != nil {
		return nil, localArchiveView{}, err
	}
	if a.Binding.ArchiveLocationHash != "" {
		return nil, localArchiveView{}, errors.New("Ternal Local supports only remote S3/GCS/Azure archive bindings, not copied filesystem archives")
	}
	resolved, err := a.MaintenanceResolved(ctx)
	if err != nil {
		return nil, localArchiveView{}, fmt.Errorf("Local maintenance is unknown: %w", err)
	}
	if !resolved {
		return nil, localArchiveView{}, errors.New("Local maintenance is held")
	}
	tip := a.Tip()
	hash, ok := a.PrefixHash(tip)
	if !ok || a.Snapshot() == nil {
		return nil, localArchiveView{}, errors.New("Local preflight requires a seeded, certified checkpoint")
	}
	return a, localArchiveView{Binding: a.Binding, Tip: tip, PrefixHash: hash, Snapshot: a.Snapshot()}, nil
}

func validateLocalBinding(c rhiza.Config, binding recovery.LocalBinding) error {
	if len(binding.Bootstrap.Members) != 1 || string(binding.Bootstrap.Members[0].ID) != c.NodeID {
		return errors.New("Local archive must bind exactly the configured singleton node")
	}
	return nil
}

func localPreflightScope() (string, error) {
	scope := os.Getenv("TERNAL_LOCAL_PREFLIGHT_SCOPE")
	if scope == "" || len(scope) > 256 || strings.TrimSpace(scope) != scope || strings.ContainsAny(scope, "\x00\r\n\t") {
		return "", errors.New("TERNAL_LOCAL_PREFLIGHT_SCOPE must be an explicit bounded Pod UID or non-Kubernetes run identifier")
	}
	return scope, nil
}

// checkArchiveMode uses only object names, never private SDK metadata formats.
// A generation root is dedicated to one mode, including after cache loss.
func checkArchiveMode(ctx context.Context, bucket objstore.Bucket, c rhiza.Config) error {
	root := path.Join(c.ObjStorePrefix, c.ClusterID) + "/"
	local := path.Join(root, "local/v1") + "/"
	return bucket.Iter(ctx, root, func(key string) error {
		isLocal := strings.HasPrefix(key, local)
		if c.Local && !isLocal {
			return errors.New("Local archive root contains non-Local objects; use fresh dedicated roots")
		}
		if !c.Local && isLocal {
			return errors.New("Local archive cannot be opened in legacy mode; preserve data and configuration")
		}
		return nil
	}, objstore.WithRecursiveIter())
}

func localTrustBeforeOpen(ctx context.Context) (*trustAnchor, error) {
	a, err := trustAnchorFromEnv()
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, errors.New("Local mode requires the Kubernetes trust anchor")
	}
	r, _, err := a.get(ctx)
	if err != nil {
		return nil, err
	}
	if r.PendingEpoch != nil {
		return nil, errors.New("Local preflight refuses a pending application write")
	}
	return a, nil
}

func localPreflightPaths(dataDir string) (string, string, error) {
	stage := os.Getenv("TERNAL_LOCAL_PREFLIGHT_STAGING_DIR")
	receipt := os.Getenv("TERNAL_LOCAL_PREFLIGHT_RECEIPT_FILE")
	data, err := filepath.Abs(dataDir)
	if err != nil {
		return "", "", err
	}
	if !filepath.IsAbs(stage) || !filepath.IsAbs(receipt) || filepath.Clean(stage) != stage || filepath.Clean(receipt) != receipt || stage == string(filepath.Separator) {
		return "", "", errors.New("Local preflight requires canonical absolute staging and receipt paths")
	}
	inside := func(child, parent string) bool {
		return child == parent || strings.HasPrefix(child, parent+string(filepath.Separator))
	}
	if filepath.Dir(receipt) != stage || inside(stage, data) || inside(data, stage) {
		return "", "", errors.New("Local staging must be separate from DataDir and contain the receipt")
	}
	info, err := os.Lstat(stage)
	if err != nil {
		return "", "", fmt.Errorf("staging must be a pre-existing dedicated directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("staging must not be a symlink")
	}
	realStage, err := filepath.EvalSymlinks(stage)
	if err != nil {
		return "", "", err
	}
	realData, err := resolvedLocalPath(data)
	if err != nil {
		return "", "", err
	}
	if inside(realStage, realData) || inside(realData, realStage) {
		return "", "", errors.New("resolved staging overlaps DataDir")
	}
	return stage, receipt, nil
}

func resolvedLocalPath(name string) (string, error) {
	real, err := filepath.EvalSymlinks(name)
	if err == nil {
		return real, nil
	}
	if !errors.Is(err, os.ErrNotExist) || filepath.Dir(name) == name {
		return "", err
	}
	parent, err := resolvedLocalPath(filepath.Dir(name))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(name)), nil
}

// LocalPreflightFromEnv downloads and verifies a published root only into staging.
// It does not open a database, initialize DataDir, pin, or mutate the archive.
func LocalPreflightFromEnv(ctx context.Context) (err error) {
	c, err := rhizaConfigFromEnv(envOr("TERNAL_DATA_DIR", "ternal-data"))
	if err != nil {
		return err
	}
	if !c.Local {
		return errors.New("local-preflight requires TERNAL_DATA_LOCAL=1")
	}
	stage, receiptPath, err := localPreflightPaths(c.DataDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	scope, err := localPreflightScope()
	if err != nil {
		return err
	}
	anchor, err := localTrustBeforeOpen(ctx)
	if err != nil {
		return err
	}
	bucket, err := recoveryStorageFromEnv().bucketWithContext(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, bucket.Close()) }()
	if err = checkArchiveMode(ctx, bucket, c); err != nil {
		return err
	}
	_, before, err := readLocalArchive(ctx, bucket, c)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp(stage, "verified-")
	if err != nil {
		return err
	}
	// Downloaded files are evidence, not a handoff. Retain only the receipt.
	defer func() { err = errors.Join(err, os.RemoveAll(work)) }()
	if err = downloadLocalRoot(ctx, bucket, localNamespace(c), before.Snapshot, work); err != nil {
		return err
	}
	_, after, err := readLocalArchive(ctx, bucket, c)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) {
		return errors.New("Local archive changed during read-only preflight")
	}
	if _, err = localTrustBeforeOpen(ctx); err != nil {
		return err
	}
	return writeLocalJSON(receiptPath, localPreflightReceipt{Version: 1, StorageID: localStorageID(c), NodeID: c.NodeID, Scope: scope, AnchorName: anchor.name, AnchorNamespace: anchor.namespace, View: before})
}

func downloadLocalRoot(ctx context.Context, bucket objstore.Bucket, prefix string, snapshot *recovery.LocalSnapshot, work string) error {
	if snapshot == nil {
		return errors.New("Local checkpoint is absent")
	}
	if err := os.Mkdir(filepath.Join(work, "files"), 0700); err != nil {
		return err
	}
	// There is no reusable cache in preflight; the published empty-cache contract
	// avoids creating a verified-block cache beneath a nonexistent directory.
	manager := checkpoint.NewManager(readOnlyLocalBucket{bucket}, prefix, "", snapshot.Seal.ConfigID)
	root, err := manager.OpenRoot(ctx, uint64(snapshot.Seal.Index), snapshot.PortableRoot)
	if err != nil {
		return err
	}
	_, err = manager.DownloadAndVerifyRootFiles(ctx, root, filepath.Join(work, "files"))
	return err
}

func readLocalJSON(name string, target any) error {
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxLocalReceipt {
		return errors.New("Local metadata must be a bounded regular file")
	}
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, maxLocalReceipt+1))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("Local metadata has trailing content")
	}
	return nil
}

func writeLocalJSON(name string, value any) (err error) {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maxLocalReceipt {
		return errors.New("Local metadata exceeds size bound")
	}
	if info, statErr := os.Lstat(name); statErr == nil && !info.Mode().IsRegular() {
		return errors.New("Local metadata target is not regular")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".local-metadata-")
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := os.Remove(f.Name()); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
			err = errors.Join(err, cleanupErr)
		}
	}()
	if _, err = io.Copy(f, bytes.NewReader(data)); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), name)
}

func checkLocalDataDir(c rhiza.Config) (*localDataMode, error) {
	name := filepath.Join(c.DataDir, localModeMarker)
	if !c.Local {
		if _, err := os.Lstat(name); err == nil {
			return nil, errors.New("Local DataDir cannot be opened in legacy mode")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, nil
	}
	info, err := os.Lstat(c.DataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("DataDir must be a dedicated real directory")
	}
	var marker localDataMode
	if err := readLocalJSON(name, &marker); err == nil {
		if marker.Version != 1 || marker.Mode != "local" || marker.StorageID != localStorageID(c) {
			return nil, errors.New("Local DataDir mode or archive identity mismatch")
		}
		return &marker, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	entries, err := os.ReadDir(c.DataDir)
	if err != nil {
		return nil, err
	}
	if len(entries) != 0 {
		return nil, errors.New("refusing unknown nonempty DataDir in Local mode; migration is unsupported")
	}
	return nil, nil
}

// prepareLocalOpen performs every mode and authority check before SDK mutation.
func prepareLocalOpen(ctx context.Context, c rhiza.Config) (err error) {
	marker, err := checkLocalDataDir(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var anchor *trustAnchor
	if !c.Local {
		// Detect a trusted Local authority even if its remote objects were lost.
		if err = legacyLocalAuthorityGuard(ctx, c); err != nil {
			return err
		}
		if c.ObjStoreProvider != "s3" && c.ObjStoreProvider != "gcs" && c.ObjStoreProvider != "azure" {
			return nil
		}
	} else {
		anchor, err = localTrustBeforeOpen(ctx)
		if err != nil {
			return err
		}
	}
	bucket, err := recoveryStorageFromEnv().bucketWithContext(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, bucket.Close()) }()
	if err = checkArchiveMode(ctx, bucket, c); err != nil {
		return err
	}
	if !c.Local {
		return nil
	}
	_, receiptPath, err := localPreflightPaths(c.DataDir)
	if err != nil {
		return err
	}
	var receipt localPreflightReceipt
	if err = readLocalJSON(receiptPath, &receipt); err != nil {
		return fmt.Errorf("read required Local preflight receipt: %w", err)
	}
	_, current, err := readLocalArchive(ctx, bucket, c)
	if err != nil {
		return err
	}
	if err := validateLocalReceipt(c, receipt, current); err != nil {
		return err
	}
	if err := validateLocalReceiptScope(receipt, anchor); err != nil {
		return err
	}
	// Init runs once per Pod, not once per app-container restart. Only immutable
	// identity is a handoff condition: SDK Open and the independent trust anchor
	// validate current history, which this same writer may legitimately advance.
	if marker != nil && !reflect.DeepEqual(marker.Binding, current.Binding) {
		return errors.New("Local DataDir binding differs from archive")
	}
	if _, err = localTrustBeforeOpen(ctx); err != nil {
		return err
	}
	if marker == nil {
		if err = os.MkdirAll(c.DataDir, 0700); err != nil {
			return err
		}
		return writeLocalJSON(filepath.Join(c.DataDir, localModeMarker), localDataMode{Version: 1, Mode: "local", StorageID: localStorageID(c), Binding: current.Binding})
	}
	return nil
}

func validateLocalReceiptScope(receipt localPreflightReceipt, anchor *trustAnchor) error {
	scope, err := localPreflightScope()
	if err != nil {
		return err
	}
	if anchor == nil || receipt.Scope != scope || receipt.AnchorName != anchor.name || receipt.AnchorNamespace != anchor.namespace {
		return errors.New("Local preflight receipt trust authority or Pod/run scope mismatch")
	}
	return nil
}

func legacyLocalAuthorityGuard(ctx context.Context, c rhiza.Config) error {
	if !requireTrustAnchor() && os.Getenv("TERNAL_TRUST_ANCHOR_CONFIGMAP") == "" {
		return nil
	}
	a, err := trustAnchorFromEnv()
	if err != nil {
		return err
	}
	if a == nil {
		return errors.New("configured trust authority is unavailable")
	}
	r, _, err := a.backend.Get(ctx)
	if err != nil {
		return fmt.Errorf("read trust authority before SDK startup: %w", err)
	}
	if r.StorageID == localStorageID(c) {
		return errors.New("trusted Local archive cannot be opened in legacy mode")
	}
	return nil
}

func validateLocalReceipt(c rhiza.Config, receipt localPreflightReceipt, current localArchiveView) error {
	if receipt.Version != 1 || receipt.StorageID != localStorageID(c) || receipt.NodeID != c.NodeID || !reflect.DeepEqual(receipt.View.Binding, current.Binding) || receipt.View.Snapshot == nil {
		return errors.New("Local preflight receipt does not match the configured archive")
	}
	return nil
}
