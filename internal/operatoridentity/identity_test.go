package operatoridentity

import (
	"os"
	"testing"
)

func TestApplyAdoptsOperatorManifestIdentity(t *testing.T) {
	// The controller rewrites exactly these when it starts a recovered
	// generation; Ternal must run that generation.
	for _, pair := range pairs {
		t.Setenv(pair[0], "operator-"+pair[1])
		t.Setenv(pair[1], "source-identity")
	}
	Apply()
	for _, pair := range pairs {
		if got := os.Getenv(pair[1]); got != "operator-"+pair[1] {
			t.Fatalf("%s = %q, want the operator-managed value", pair[1], got)
		}
	}
}

func TestApplyIsNoopWithoutOperator(t *testing.T) {
	for _, pair := range pairs {
		// Unset is what an unmanaged pod has; Apply treats empty the same way.
		t.Setenv(pair[0], "")
		t.Setenv(pair[1], "ternals-own-value")
	}
	Apply()
	for _, pair := range pairs {
		if got := os.Getenv(pair[1]); got != "ternals-own-value" {
			t.Fatalf("%s = %q, want Ternal's own value untouched", pair[1], got)
		}
	}
}

func TestApplyNeverClearsWithEmptyOperatorValues(t *testing.T) {
	// An empty operator variable must not erase the configured identity: the
	// object-store durability and cluster ID decide which generation is served.
	for _, pair := range pairs {
		t.Setenv(pair[0], "")
		t.Setenv(pair[1], "ternals-own-value")
	}
	Apply()
	for _, pair := range pairs {
		if got := os.Getenv(pair[1]); got != "ternals-own-value" {
			t.Fatalf("%s = %q, want Ternal's own value preserved", pair[1], got)
		}
	}
}
