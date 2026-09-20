// Package operatoridentity applies the recovery operator's generation identity
// to Ternal's own configuration variables.
//
// The upstream controller rewrites the identity variables of the target
// container when it starts a recovered generation, and Ternal has to run that
// generation, so the rewrite has to reach Ternal's configuration. This package
// is the only place in Ternal where the upstream variable names exist: it maps
// them onto TERNAL_* names at process start, before anything reads
// configuration. Operators of Ternal never set them; the values are derived
// from Ternal's own settings and only differ during a recovery transition.
// scripts/check-public-config.sh exempts this file.
package operatoridentity

import "os"

// pairs holds the operator-managed variable and the Ternal configuration
// variable it carries. The set is exactly what the controller rewrites to start
// a recovered generation.
var pairs = [][2]string{
	{"RHIZA_CLUSTER_ID", "TERNAL_DATA_CLUSTER_ID"},
	{"RHIZA_CLUSTER_MEMBERS", "TERNAL_DATA_CLUSTER_MEMBERS"},
	{"RHIZA_ADMIN_TOKEN", "TERNAL_DATA_ADMIN_TOKEN"},
	{"RHIZA_OBJSTORE_DURABILITY", "TERNAL_OBJECT_STORE_DURABILITY"},
}

// Apply copies every present operator-managed variable onto its Ternal
// counterpart. Without an operator in the pod nothing is set and it is a no-op;
// an empty value is never allowed to clear Ternal's own configuration.
func Apply() {
	for _, pair := range pairs {
		if value := os.Getenv(pair[0]); value != "" {
			_ = os.Setenv(pair[1], value)
		}
	}
}
