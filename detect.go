package pnpm

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/paketo-buildpacks/packit/v2"
)

// detectPackageJSON represents the minimal structural subset of package.json
// needed to extract package manager constraints during detection.
type detectPackageJSON struct {
	PackageManager string `json:"packageManager"`
}

// parseLockfileVersion reads pnpm-lock.yaml line-by-line to extract the lockfile version
// without introducing heavy third-party YAML parser dependencies.
func parseLockfileVersion(workingDir string) string {
	lockPath := filepath.Join(workingDir, "pnpm-lock.yaml")
	file, err := os.Open(lockPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "lockfileVersion:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "lockfileVersion:"))
			val = strings.Trim(val, `'"`)
			return val
		}
	}
	return ""
}

// Detect returns a packit.DetectFunc that advertises the provision of "pnpm".
// It resolves the requested pnpm version with the following precedence:
// 1. Explicit BP_PNPM_VERSION env variable override.
// 2. packageManager field in package.json.
// 3. Auto-detected lockfileVersion in pnpm-lock.yaml.
// 4. Default version specified in buildpack.toml.
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
			// Priority 2: Inspect package.json for "packageManager"
			var pkgVersion string
			pkgPath := filepath.Join(context.WorkingDir, "package.json")
			file, err := os.Open(pkgPath)
			if err == nil {
				defer file.Close()

				var pkg detectPackageJSON
				if err := json.NewDecoder(file).Decode(&pkg); err == nil {
					if strings.HasPrefix(pkg.PackageManager, "pnpm@") {
						pkgVersion = strings.TrimPrefix(pkg.PackageManager, "pnpm@")
					}
				}
			}

			if pkgVersion != "" {
				requirements = append(requirements, packit.BuildPlanRequirement{
					Name: PNPMDependency,
					Metadata: map[string]interface{}{
						"version":        pkgVersion,
						"version-source": "package.json",
					},
				})
			} else {
				// Priority 3: Auto-detect from pnpm-lock.yaml version mapping
				lockVersion := parseLockfileVersion(context.WorkingDir)
				var mappedVersion string

				// Checks both exact versions (e.g. "6", "9", "10") and prefixed versions (e.g. "6.0", "9.0", "10.0")
				if lockVersion == "6" || strings.HasPrefix(lockVersion, "6.") {
					mappedVersion = "8.15.9" // lockfile v6.0 maps to pnpm v8
				} else if lockVersion == "9" || strings.HasPrefix(lockVersion, "9.") {
					mappedVersion = "9.1.0" // lockfile v9.0 maps to pnpm v9
				} else if lockVersion == "10" || strings.HasPrefix(lockVersion, "10.") {
					mappedVersion = "10.0.0" // lockfile v10.0 maps to pnpm v10
				}

				if mappedVersion != "" {
					requirements = append(requirements, packit.BuildPlanRequirement{
						Name: PNPMDependency,
						Metadata: map[string]interface{}{
							"version":        mappedVersion,
							"version-source": "pnpm-lock.yaml",
						},
					})
				}
			}
		}

		// Priority 4: Default Fallback (Triggered during build phase if requirements is empty)

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
