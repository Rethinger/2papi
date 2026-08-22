// Package edition reports which product edition is running.
//
// One binary, three editions (see plan/2papi-3-editions-strategy.md):
//   - oss   — open-source build, default;
//   - cloud — 2papi Cloud hosted build (enables tenant/billing paths);
//   - ent   — enterprise build (license key required).
//
// Detection order:
//  1. 2PAPI_EDITION env ("oss" | "cloud" | "ent") — wins always;
//  2. signed license file ("2papi.license", Ed25519, see internal/license)
//     whose payload edition is "cloud:" / "ent:";
//  3. otherwise OSS.
package edition

import (
	"os"
	"strings"
	"time"

	"github.com/Rethinger/2papi/internal/license"
)

const (
	OSS   = "oss"
	Cloud = "cloud"
	ENT   = "ent"

	// EnvVar overrides detection for tests and deployments.
	EnvVar = "2PAPI_EDITION"

	// LicenseFile is probed when the env var is unset.
	LicenseFile = "2papi.license"
)

// Active returns the current edition id (oss|cloud|ent). It never fails:
// anything unrecognized degrades to OSS so a stray env value can never
// accidentally unlock paid code paths.
func Active() string {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvVar))); v != "" {
		if v == Cloud || v == ENT {
			return v
		}
		return OSS
	}
	data, err := os.ReadFile(LicenseFile)
	if err == nil {
		// Signed license: prefix must match payload edition and the
		// Ed25519 signature must verify against the trusted key.
		// Anything invalid (garbage/expired/wrong key) degrades to OSS.
		if lic, lerr := license.Validate(string(data), time.Now()); lerr == nil {
			switch lic.Edition {
			case Cloud:
				return Cloud
			case ENT:
				return ENT
			}
		}
	}
	return OSS
}

// IsOSS reports whether this is the plain open-source edition.
func IsOSS() bool { return Active() == OSS }

// IsCloud reports whether cloud-only code paths may run.
func IsCloud() bool { return Active() == Cloud }

// IsEnterprise reports whether enterprise-licensed paths may run.
func IsEnterprise() bool { return Active() == ENT }
