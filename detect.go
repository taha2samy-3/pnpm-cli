package pnpm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/paketo-buildpacks/packit/v2"
)

// packageJSON represents the minimal structural subset of package.json
// needed to extract package manager constraints during detection.
type packageJSON struct {
	PackageManager string            `json:"packageManager"`
	Bin            map[string]string `json:"bin"`
}

// Detect returns a packit.DetectFunc that advertises the provision of "pnpm".
// It evaluates explicit version requests via environment variables or package.json
// declarations. If no version is specified, it allows the build phase to fall back
// to the default version configured in buildpack.toml.
func Detect() packit.DetectFunc {
	return func(context packit.DetectContext) (packit.DetectResult, error) {
		var requirements []packit.BuildPlanRequirement

		// Priority 1: Check for explicit environment variable override
		if envVersion := os.Getenv("BP_PNPM_VERSION"); envVersion != "" {
			requirements = append(requirements, packit.BuildPlanRequirement{
				Name: PNPMDependency,
				Metadata: map[string]interface{}{
					"version":        envVersion,
					"version-source": "BP_PNPM_VERSION",
				},
			})
		} else {
			// Priority 2: Inspect package.json for "packageManager": "pnpm@<version>"
			pkgPath := filepath.Join(context.WorkingDir, "package.json")
			file, err := os.Open(pkgPath)
			if err == nil {
				defer file.Close()

				var pkg packageJSON
				if err := json.NewDecoder(file).Decode(&pkg); err == nil {
					if strings.HasPrefix(pkg.PackageManager, "pnpm@") {
						version := strings.TrimPrefix(pkg.PackageManager, "pnpm@")
						requirements = append(requirements, packit.BuildPlanRequirement{
							Name: PNPMDependency,
							Metadata: map[string]interface{}{
								"version":        version,
								"version-source": "package.json",
							},
						})
					}
				}
			}
		}

		// Priority 3: Default Fallback
		// If neither BP_PNPM_VERSION nor packageManager provides a constraint,
		// 'requirements' remains unconstrained. During the build phase,
		// draft.Planner will fail to find a version metadata key and will
		// default to "default", selecting the default version from buildpack.toml.

		return packit.DetectResult{
			Plan: packit.BuildPlan{
				Provides: []packit.BuildPlanProvision{
					{Name: PNPMDependency},
				},
				Requires: requirements,
			},
		}, nil
	}
}
