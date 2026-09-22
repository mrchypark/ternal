package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mrchypark/rhiza/pkg/recoveryanchor"
)

// recoveryNull is the canonical serialization of an absent recovery projection.
// Admission rules compare it as a plain string, so the absence has to be
// spelled out rather than left empty.
const recoveryNull = "null"

func recoveryProjection[T any](value *T) string {
	if value == nil {
		return recoveryNull
	}
	data, err := json.Marshal(value)
	if err != nil {
		return recoveryNull
	}
	return string(data)
}

func parseRecoveryProjection[T any](raw string) (*T, error) {
	if raw == recoveryNull {
		return nil, nil
	}
	var value T
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("parse trust-anchor recovery projection")
	}
	if recoveryProjection(&value) != raw {
		return nil, fmt.Errorf("non-canonical trust-anchor recovery projection")
	}
	return &value, nil
}

// recoveryRecord projects the composite ConfigMap state onto the common record
// shared with the anchor service.  PendingWrite mirrors the application
// reservation, which is what recovery activation must not disturb.
func recoveryRecord(record trustAnchorRecord) recoveryanchor.Record {
	return recoveryanchor.Record{
		Version:        recoveryanchor.Version1,
		EvidenceFormat: trustAnchorEvidenceFormat,
		Binding:        recoveryanchor.Binding{ClusterID: record.ClusterID, StorageID: record.StorageID},
		Evidence:       record.evidence().bytes(),
		PendingWrite:   record.PendingEpoch != nil,
		Generation:     record.Generation,
		Transition:     record.Transition,
		LastTransition: record.Receipt,
	}
}

// anchorRecordFromRecovery is the inverse projection.  The flat application
// fields come back from the frozen evidence, never from a fresh derivation.
func anchorRecordFromRecovery(record recoveryanchor.Record) (trustAnchorRecord, error) {
	if record.Version != recoveryanchor.Version1 || record.EvidenceFormat != trustAnchorEvidenceFormat {
		return trustAnchorRecord{}, fmt.Errorf("unsupported recovery record")
	}
	evidence, err := parseTrustAnchorEvidence(record.Evidence)
	if err != nil {
		return trustAnchorRecord{}, err
	}
	next := trustAnchorRecord{
		Format:       evidence.Format,
		ClusterID:    record.Binding.ClusterID,
		StorageID:    record.Binding.StorageID,
		Epoch:        evidence.Epoch,
		Token:        evidence.Token,
		PendingEpoch: evidence.PendingEpoch,
		PendingID:    evidence.PendingID,
		Bootstrap:    evidence.Bootstrap,
		Generation:   record.Generation,
		Transition:   record.Transition,
		Receipt:      record.LastTransition,
	}
	if record.PendingWrite != (next.PendingEpoch != nil) {
		return trustAnchorRecord{}, fmt.Errorf("recovery record disagrees with its evidence")
	}
	return next, nil
}

// recoveryAnchorBackend adapts the pre-existing Ternal ConfigMap to the anchor
// service's backend interface.  It deliberately does not check the deployment
// binding: the whole point of activation is that the binding legitimately
// changes, and the anchor service must still be able to read and settle the
// record it just committed.
type recoveryAnchorBackend struct {
	anchor trustAnchorBackend
	id     string
}

// NewRecoveryAnchorBackend maps an anchor ID onto the existing trust-anchor
// ConfigMap.  The configured name is kept as-is; adopting the backend interface
// does not adopt another naming convention.
func NewRecoveryAnchorBackend(anchorID, name, namespace string) (recoveryanchor.Backend, error) {
	if anchorID == "" || name == "" || namespace == "" {
		return nil, fmt.Errorf("recovery anchor requires an id, configmap name, and namespace")
	}
	return &recoveryAnchorBackend{anchor: newKubernetesTrustAnchor(name, namespace), id: anchorID}, nil
}

func (b *recoveryAnchorBackend) checkID(id string) error {
	if id != b.id {
		return fmt.Errorf("unknown recovery anchor")
	}
	return nil
}

func (b *recoveryAnchorBackend) Read(ctx context.Context, id string) (recoveryanchor.Record, string, error) {
	if err := b.checkID(id); err != nil {
		return recoveryanchor.Record{}, "", err
	}
	record, rv, err := b.anchor.Get(ctx)
	if err != nil {
		return recoveryanchor.Record{}, "", err
	}
	if err := record.validateStructure(); err != nil {
		return recoveryanchor.Record{}, "", err
	}
	return recoveryRecord(record), rv, nil
}

func (b *recoveryAnchorBackend) CAS(ctx context.Context, id, version string, record recoveryanchor.Record) error {
	if err := b.checkID(id); err != nil {
		return err
	}
	next, err := anchorRecordFromRecovery(record)
	if err != nil {
		return err
	}
	if err := next.validateStructure(); err != nil {
		return err
	}
	_, err = b.anchor.CAS(ctx, version, next)
	return err
}
