package pnpm

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/paketo-buildpacks/packit/v2"
)

//go:embed dependencies.json
var embeddedDependenciesJSON []byte

// pnpmDependencyEntry represents the minimal version structure read from embedded JSON.
type pnpmDependencyEntry struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// detectPackageJSON represents the minimal structural subset of package.json.
type detectPackageJSON struct {
	PackageManager string `json:"packageManager"`
}

// parseLockfileVersion reads pnpm-lock.yaml line-by-line to extract lockfileVersion.
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

// loadAllVersions parses the embedded JSON into a slice of valid semver Versions.
func loadAllVersions() []*semver.Version {
	var entries []pnpmDependencyEntry
	if err := json.Unmarshal(embeddedDependenciesJSON, &entries); err != nil {
		return nil
	}

	var versions []*semver.Version
	for _, entry := range entries {
		if entry.ID == PNPMDependency || entry.ID == "" {
			if sv, err := semver.NewVersion(entry.Version); err == nil {
				versions = append(versions, sv)
			}
		}
	}
	return versions
}

// findHighestInMajor returns the highest version string available in embedded JSON for a Major family.
func findHighestInMajor(major uint64, versions []*semver.Version) string {
	var highest *semver.Version
	for _, sv := range versions {
		if sv.Major() == major {
			if highest == nil || sv.GreaterThan(highest) {
				highest = sv
			}
		}
	}
	if highest != nil {
		return highest.String()
	}
	return ""
}

// resolveVersion dynamically matches requested version against the hundreds of versions in JSON:
// 1. Checks exact match.
// 2. Fallbacks to highest version in same Major family.
// 3. Evaluates semver constraints (e.g. "9.*", ">=10").
func resolveVersion(requested string, versions []*semver.Version) string {
	cleanReq := strings.TrimPrefix(requested, "pnpm@")
	cleanReq = strings.TrimSpace(cleanReq)
	if cleanReq == "" {
		return ""
	}

	// 1. Check Exact Match
	for _, sv := range versions {
		if sv.String() == cleanReq || sv.Original() == cleanReq {
			return cleanReq
		}
	}

	// 2. Fallback to Highest in Same Major Family
	if reqSv, err := semver.NewVersion(cleanReq); err == nil {
		bestInMajor := findHighestInMajor(reqSv.Major(), versions)
		if bestInMajor != "" {
			return bestInMajor
		}
	}

	// 3. Semver Constraint Evaluation
	if constraint, err := semver.NewConstraint(cleanReq); err == nil {
		var highestMatch *semver.Version
		for _, sv := range versions {
			if constraint.Check(sv) {
				if highestMatch == nil || sv.GreaterThan(highestMatch) {
					highestMatch = sv
				}
			}
		}
		if highestMatch != nil {
			return highestMatch.String()
		}
	}

	return cleanReq
}

// lockfileVersionToMajor dynamically converts lockfileVersion string to PNPM Major Version:
// lockfile 5.x -> Major 7
// lockfile 6.x -> Major 8
// lockfile N.x (N >= 9) -> Major N
func lockfileVersionToMajor(lockVersion string) uint64 {
	parts := strings.Split(lockVersion, ".")
	majorInt, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0
	}

	if majorInt == 5 {
		return 7
	}
	if majorInt == 6 {
		return 8
	}
	if majorInt >= 9 {
		return majorInt
	}
	return 0
}

// Detect returns a packit.DetectFunc that advertises the provision of "pnpm".
func Detect() packit.DetectFunc {
	return func(context packit.DetectContext) (packit.DetectResult, error) {
		var requirements []packit.BuildPlanRequirement
		allVersions := loadAllVersions()

		// Priority 1: Check for explicit environment variable override
		if envVersion := os.Getenv("BP_PNPM_VERSION"); envVersion != "" {
			resolvedVersion := resolveVersion(envVersion, allVersions)
			requirements = append(requirements, packit.BuildPlanRequirement{
				Name: PNPMDependency,
				Metadata: map[string]interface{}{
					"version":        resolvedVersion,
					"version-source": "BP_PNPM_VERSION",
				},
			})
		} else {
			// Priority 2: Inspect package.json for "packageManager"
			var pkgVersion string
			pkgPath := filepath.Join(context.WorkingDir, "package.json")
			if file, err := os.Open(pkgPath); err == nil {
				defer file.Close()
				var pkg detectPackageJSON
				if err := json.NewDecoder(file).Decode(&pkg); err == nil {
					if strings.HasPrefix(pkg.PackageManager, "pnpm@") {
						pkgVersion = strings.TrimPrefix(pkg.PackageManager, "pnpm@")
					}
				}
			}

			if pkgVersion != "" {
				resolvedVersion := resolveVersion(pkgVersion, allVersions)
				requirements = append(requirements, packit.BuildPlanRequirement{
					Name: PNPMDependency,
					Metadata: map[string]interface{}{
						"version":        resolvedVersion,
						"version-source": "package.json",
					},
				})
			} else {
				// Priority 3: Auto-detect from pnpm-lock.yaml version mapping
				lockVersionStr := parseLockfileVersion(context.WorkingDir)
				if lockVersionStr != "" {
					major := lockfileVersionToMajor(lockVersionStr)
					if major > 0 {
						resolvedVersion := findHighestInMajor(major, allVersions)
						if resolvedVersion != "" {
							requirements = append(requirements, packit.BuildPlanRequirement{
								Name: PNPMDependency,
								Metadata: map[string]interface{}{
									"version":        resolvedVersion,
									"version-source": "pnpm-lock.yaml",
								},
							})
						}
					}
				}
			}
		}

		// Priority 4: Default Fallback dynamically to absolute latest version in JSON
		if len(requirements) == 0 && len(allVersions) > 0 {
			var latest *semver.Version
			for _, sv := range allVersions {
				if latest == nil || sv.GreaterThan(latest) {
					latest = sv
				}
			}
			if latest != nil {
				requirements = append(requirements, packit.BuildPlanRequirement{
					Name: PNPMDependency,
					Metadata: map[string]interface{}{
						"version":        latest.String(),
						"version-source": "default",
					},
				})
			}
		}

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
