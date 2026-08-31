package store

import "testing"

func TestRhizaConfigFailsClosedOnInvalidDurationAndUndersizedCluster(t *testing.T) {
	t.Setenv("TERNAL_OBJECT_STORE_SYNC_INTERVAL", "invalid")
	if _, err := rhizaConfigFromEnv(t.TempDir()); err == nil {
		t.Fatal("invalid duration accepted")
	}
	t.Setenv("TERNAL_OBJECT_STORE_SYNC_INTERVAL", "1m")
	t.Setenv("TERNAL_DATA_MULTI_NODE", "1")
	t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", `[{"id":"only"}]`)
	if _, err := rhizaConfigFromEnv(t.TempDir()); err == nil {
		t.Fatal("undersized multi-node cluster accepted")
	}
}
