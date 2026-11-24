package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type upgradeFixture struct {
	HostID, PolicyID string
	Anchor           trustAnchorRecord
	Version          int64
}

// The provider runner compiles this same Ternal fixture against each exact
// dependency version. It never uses an operational archive or user database.
func TestRhizaUpgradeFixture(t *testing.T) {
	phase := os.Getenv("TERNAL_UPGRADE_PHASE")
	if phase == "" {
		t.Skip("cross-version fixture is run by object-store-contract.sh")
	}
	if phase != "seed" && phase != "verify" {
		t.Fatal("unknown upgrade fixture phase")
	}
	wantVersion := os.Getenv("TERNAL_UPGRADE_EXPECTED_VERSION")
	if wantVersion != "v0.21.2" && wantVersion != "v0.27.4" {
		t.Fatal("fixture requires an exact supported dependency version")
	}
	build, ok := debug.ReadBuildInfo()
	if !ok || build.GoVersion != "go1.27.4" {
		t.Fatal("fixture was not built with Go 1.27.4")
	}
	found := false
	for _, dependency := range build.Deps {
		if dependency.Path == "github.com/mrchypark/rhiza" {
			found = dependency.Version == wantVersion && dependency.Replace == nil
		}
	}
	if !found {
		t.Fatal("fixture dependency version or replacement differs from the expected release")
	}
	ctx, _ := providerContractSetup(t)
	root := os.Getenv("TERNAL_UPGRADE_ROOT")
	if root == "" || !filepath.IsAbs(root) {
		t.Fatal("fixture requires an isolated absolute root")
	}
	parent, err := os.Stat(filepath.Dir(root))
	info, rootErr := os.Lstat(root)
	resolved, resolveErr := filepath.EvalSymlinks(root)
	if err != nil || rootErr != nil || resolveErr != nil || !parent.IsDir() || parent.Mode().Perm() != 0700 || !info.IsDir() || info.Mode().Perm() != 0700 || resolved != root || !strings.HasPrefix(filepath.Base(filepath.Dir(root)), "ternal-provider-"+os.Getenv("TERNAL_TEST_PROVIDER")+".") {
		t.Fatal("fixture root must be a private directory in the runner's disposable tree")
	}
	dataDir := filepath.Join(root, "data")
	if os.Getenv("TERNAL_UPGRADE_COLD") == "1" {
		dataDir = filepath.Join(root, "cold-data")
		if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
			t.Fatalf("cold restore root must not exist: %v", err)
		}
	}
	prefix := os.Getenv("TERNAL_UPGRADE_PREFIX")
	if !strings.HasPrefix(prefix, "upgrade/") || strings.ContainsAny(prefix, "\\\r\n\t ") {
		t.Fatal("fixture requires a disposable upgrade prefix")
	}
	t.Setenv("TERNAL_OBJECT_STORE_PREFIX", prefix)
	t.Setenv("TERNAL_DATA_CLUSTER_ID", "ternal-upgrade-"+os.Getenv("TERNAL_TEST_PROVIDER"))
	t.Setenv("TERNAL_TRUST_ANCHOR_CONFIGMAP", "upgrade-contract")
	t.Setenv("TERNAL_TRUST_ANCHOR_NAMESPACE", "test")
	fixtureFile := filepath.Join(root, "fixture.json")
	fixture := upgradeFixture{}
	if phase == "seed" {
		if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
			t.Fatal("seed fixture must not open an existing DataDir")
		}
		if _, err := os.Stat(fixtureFile); !os.IsNotExist(err) {
			t.Fatal("seed fixture must not overwrite existing evidence")
		}
		fixture.Anchor = storageBindingFromEnv()
		fixture.Anchor.Token, fixture.Anchor.Bootstrap = uuid.NewString(), true
	} else {
		data, err := os.ReadFile(fixtureFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &fixture); err != nil {
			t.Fatal(err)
		}
	}
	anchor := &memoryAnchor{record: fixture.Anchor, rv: fixture.Version}
	previous := kubernetesAnchorBackend
	kubernetesAnchorBackend = func(string, string) trustAnchorBackend { return anchor }
	t.Cleanup(func() { kubernetesAnchorBackend = previous })
	current, err := Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if current != nil {
			if err := current.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	if err := current.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	const actor = "upgrade-fixture"
	if phase == "seed" {
		host, err := current.CreateHost(ctx, NewHost{Name: "old-host", EndpointID: strings.Repeat("d", 64), SSHUser: "ops", SSHPort: 2222}, actor)
		if err != nil {
			t.Fatal(err)
		}
		fixture.HostID = host.ID
		policy, err := current.CreatePolicy(ctx, NewPolicy{Name: "old-policy", Principal: "groups:upgrade", HostSelector: "env=upgrade", SSHUsers: []string{"ops"}}, actor)
		if err != nil {
			t.Fatal(err)
		}
		fixture.PolicyID = policy.ID
	} else {
		host, err := current.GetHost(ctx, fixture.HostID)
		if err != nil || host == nil || host.Name != "old-host" || host.SSHUser != "ops" {
			t.Fatalf("upgrade lost old application host: %#v %v", host, err)
		}
		policies, err := current.ListPolicies(ctx)
		if err != nil || len(policies) != 1 || policies[0].ID != fixture.PolicyID || policies[0].Name != "old-policy" || policies[0].Principal != "groups:upgrade" || policies[0].HostSelector != "env=upgrade" || !reflect.DeepEqual(policies[0].SSHUsers, []string{"ops"}) {
			t.Fatalf("upgrade lost old application policy: %#v %v", policies, err)
		}
		if err := current.UpdateHost(ctx, fixture.HostID, NewHost{Name: "new-host", EndpointID: host.EndpointID, SSHUser: "ops", SSHPort: 2222}, actor); err != nil {
			t.Fatal(err)
		}
		if err := current.UpdatePolicy(ctx, fixture.PolicyID, NewPolicy{Name: "new-policy", Principal: "groups:upgrade", HostSelector: "env=upgrade", SSHUsers: []string{"ops"}}, actor); err != nil {
			t.Fatal(err)
		}
		host, err = current.GetHost(ctx, fixture.HostID)
		if err != nil || host == nil || host.Name != "new-host" {
			t.Fatalf("post-upgrade host write failed: %#v %v", host, err)
		}
		policies, err = current.ListPolicies(ctx)
		if err != nil || len(policies) != 1 || policies[0].Name != "new-policy" {
			t.Fatalf("post-upgrade policy write failed: %#v %v", policies, err)
		}
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	current = nil
	if phase == "seed" {
		fixture.Anchor, fixture.Version = anchor.record, anchor.rv
		data, err := json.Marshal(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixtureFile, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("Ternal fixture %s passed: dependency=%s Go=%s cold=%t", phase, wantVersion, build.GoVersion, os.Getenv("TERNAL_UPGRADE_COLD") == "1")
}
