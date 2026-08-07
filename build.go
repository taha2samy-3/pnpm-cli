package pnpm

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/paketo-buildpacks/packit/v2"
	"github.com/paketo-buildpacks/packit/v2/chronos"
	"github.com/paketo-buildpacks/packit/v2/draft"
	"github.com/paketo-buildpacks/packit/v2/postal"
	"github.com/paketo-buildpacks/packit/v2/sbom"
	"github.com/paketo-buildpacks/packit/v2/scribe"
)

// jsonDependency represents the full dependency metadata structure in embedded JSON.
type jsonDependency struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	URI             string   `json:"uri"`
	Checksum        string   `json:"checksum"`
	CPE             string   `json:"cpe"`
	PURL            string   `json:"purl"`
	Source          string   `json:"source"`
	SourceChecksum  string   `json:"source_checksum"`
	Stacks          []string `json:"stacks"`
	StripComponents int      `json:"strip-components"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
}

//go:generate faux --interface DependencyManager --output fakes/dependency_manager.go
type DependencyManager interface {
	Resolve(path, id, version, stack string) (postal.Dependency, error)
	Deliver(dependency postal.Dependency, cnbPath, layerPath, platformPath string) error
	GenerateBillOfMaterials(dependencies ...postal.Dependency) []packit.BOMEntry
}

//go:generate faux --interface SBOMGenerator --output fakes/sbom_generator.go
type SBOMGenerator interface {
	GenerateFromDependency(dependency postal.Dependency, dir string) (sbom.SBOM, error)
}

// buildPackageJSON is a minimal representation of the pnpm package.json.
type buildPackageJSON struct {
	Bin map[string]string `json:"bin"`
}

// parsePnpmEntryPoint reads <layerPath>/package.json and returns the absolute path to pnpm entry-point.
func parsePnpmEntryPoint(layerPath string) (string, error) {
	pkgJSONPath := filepath.Join(layerPath, "package.json")

	data, err := os.ReadFile(pkgJSONPath)
	if err != nil {
		return "", fmt.Errorf("failed to read package.json from pnpm layer (%s): %w", pkgJSONPath, err)
	}

	var pkg buildPackageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return "", fmt.Errorf("failed to parse package.json at %s: %w", pkgJSONPath, err)
	}

	relEntry, ok := pkg.Bin["pnpm"]
	if !ok || relEntry == "" {
		return "", fmt.Errorf(
			"package.json at %s does not declare a 'pnpm' entry under 'bin' (found: %v)",
			pkgJSONPath, pkg.Bin,
		)
	}

	return filepath.Join(layerPath, relEntry), nil
}

// convertToPostalDependency converts our embedded jsonDependency struct into postal.Dependency.
func convertToPostalDependency(dep jsonDependency) postal.Dependency {
	stacks := dep.Stacks
	if len(stacks) == 0 {
		stacks = []string{"*"}
	}
	stripComp := dep.StripComponents
	if stripComp == 0 {
		stripComp = 1
	}
	return postal.Dependency{
		ID:              dep.ID,
		Name:            dep.Name,
		Version:         dep.Version,
		URI:             dep.URI,
		Checksum:        dep.Checksum,
		CPE:             dep.CPE,
		PURL:            dep.PURL,
		Source:          dep.Source,
		SourceChecksum:  dep.SourceChecksum,
		Stacks:          stacks,
		StripComponents: stripComp,
	}
}

// resolveEmbeddedDependency uses the package-level embeddedDependenciesJSON (declared in detect.go).
func resolveEmbeddedDependency(requestedVersion, targetArch, targetOS string) (postal.Dependency, error) {
	var jsonDeps []jsonDependency
	if err := json.Unmarshal(embeddedDependenciesJSON, &jsonDeps); err != nil {
		return postal.Dependency{}, fmt.Errorf("failed to unmarshal embedded dependencies JSON: %w", err)
	}

	var validVersions []*semver.Version
	depMap := make(map[string]jsonDependency)

	for _, dep := range jsonDeps {
		// Filter by architecture and OS match
		if (dep.Arch == "" || dep.Arch == targetArch) && (dep.OS == "" || dep.OS == targetOS) {
			if sv, err := semver.NewVersion(dep.Version); err == nil {
				validVersions = append(validVersions, sv)
				depMap[sv.String()] = dep
				depMap[dep.Version] = dep
			}
		}
	}

	if len(validVersions) == 0 {
		return postal.Dependency{}, fmt.Errorf("no dependencies found in embedded JSON for arch: %s, os: %s", targetArch, targetOS)
	}

	cleanReq := strings.TrimPrefix(requestedVersion, "pnpm@")
	cleanReq = strings.TrimSpace(cleanReq)

	// 1. Direct Match Check
	if dep, ok := depMap[cleanReq]; ok {
		return convertToPostalDependency(dep), nil
	}

	// 2. Semver Match / Highest in Major Family
	if reqSv, err := semver.NewVersion(cleanReq); err == nil {
		var bestInMajor *semver.Version
		for _, sv := range validVersions {
			if sv.Major() == reqSv.Major() {
				if bestInMajor == nil || sv.GreaterThan(bestInMajor) {
					bestInMajor = sv
				}
			}
		}
		if bestInMajor != nil {
			return convertToPostalDependency(depMap[bestInMajor.String()]), nil
		}
	}

	// 3. Range or Default Constraint Match (e.g. "9.*", "default")
	reqConstraint := cleanReq
	if cleanReq == "default" || cleanReq == "" {
		reqConstraint = "*"
	}
	if constraint, err := semver.NewConstraint(reqConstraint); err == nil {
		var bestConstraintMatch *semver.Version
		for _, sv := range validVersions {
			if constraint.Check(sv) {
				if bestConstraintMatch == nil || sv.GreaterThan(bestConstraintMatch) {
					bestConstraintMatch = sv
				}
			}
		}
		if bestConstraintMatch != nil {
			return convertToPostalDependency(depMap[bestConstraintMatch.String()]), nil
		}
	}

	// 4. Absolute Latest Version Fallback
	var absoluteLatest *semver.Version
	for _, sv := range validVersions {
		if absoluteLatest == nil || sv.GreaterThan(absoluteLatest) {
			absoluteLatest = sv
		}
	}
	if absoluteLatest != nil {
		return convertToPostalDependency(depMap[absoluteLatest.String()]), nil
	}

	return postal.Dependency{}, fmt.Errorf("could not resolve pnpm dependency for requested version '%s'", requestedVersion)
}

func Build(
	dependencyManager DependencyManager,
	sbomGenerator SBOMGenerator,
	clock chronos.Clock,
	logger scribe.Emitter,
) packit.BuildFunc {
	return func(context packit.BuildContext) (packit.BuildResult, error) {
		logger.Title("%s %s", context.BuildpackInfo.Name, context.BuildpackInfo.Version)

		pnpmLayer, err := context.Layers.Get(PNPMLayerName)
		if err != nil {
			return packit.BuildResult{}, err
		}

		planner := draft.NewPlanner()
		entry, _ := planner.Resolve(PNPMDependency, context.Plan.Entries, nil)
		version, ok := entry.Metadata["version"].(string)
		if !ok || version == "" {
			version = "default"
		}

		// Prioritize explicit build-time environment variable overrides (highest precedence)
		if envVersion := os.Getenv("BP_PNPM_VERSION"); envVersion != "" {
			version = envVersion
		}

		// Target Arch and OS Resolution
		targetArch := context.TargetInfo.Arch
		if targetArch == "" {
			targetArch = "amd64"
		}
		targetOS := context.TargetInfo.OS
		if targetOS == "" {
			targetOS = "linux"
		}

		// Resolve dependency directly from embedded JSON metadata (declared in detect.go)
		dependency, err := resolveEmbeddedDependency(version, targetArch, targetOS)
		if err != nil {
			// Fallback attempt via dependencyManager in case buildpack.toml has entries
			dependency, err = dependencyManager.Resolve(
				filepath.Join(context.CNBPath, "buildpack.toml"),
				PNPMDependency,
				version,
				context.Stack)
			if err != nil {
				return packit.BuildResult{}, fmt.Errorf("failed to resolve pnpm dependency: %w", err)
			}
		}

		bom := dependencyManager.GenerateBillOfMaterials(dependency)

		launch, build := planner.MergeLayerTypes(PNPMDependency, context.Plan.Entries)

		var buildMetadata = packit.BuildMetadata{}
		var launchMetadata = packit.LaunchMetadata{}
		if build {
			buildMetadata = packit.BuildMetadata{BOM: bom}
		}

		if launch {
			launchMetadata = packit.LaunchMetadata{BOM: bom}
		}

		cachedSHA, ok := pnpmLayer.Metadata[DependencyCacheKey].(string)
		if ok && postal.Checksum(dependency.Checksum).MatchString(cachedSHA) {
			logger.Process("Reusing cached layer %s", pnpmLayer.Path)
			logger.Break()

			pnpmLayer.Launch, pnpmLayer.Build, pnpmLayer.Cache = launch, build, build

			return packit.BuildResult{
				Layers: []packit.Layer{pnpmLayer},
				Build:  buildMetadata,
				Launch: launchMetadata,
			}, nil
		}

		logger.Process("Executing build process")

		pnpmLayer, err = pnpmLayer.Reset()
		if err != nil {
			return packit.BuildResult{}, err
		}

		pnpmLayer.Launch, pnpmLayer.Build, pnpmLayer.Cache = launch, build, build

		logger.Subprocess("Installing pnpm %s", dependency.Version)

		duration, err := clock.Measure(func() error {
			return dependencyManager.Deliver(dependency, context.CNBPath, pnpmLayer.Path, context.Platform.Path)
		})
		if err != nil {
			return packit.BuildResult{}, err
		}

		// ── Dynamic shim creation ──────────────────────────────────────────────
		entryPoint, err := parsePnpmEntryPoint(pnpmLayer.Path)
		if err != nil {
			return packit.BuildResult{}, fmt.Errorf("could not determine pnpm entry point: %w", err)
		}

		pnpmBinDir := filepath.Join(pnpmLayer.Path, "bin")
		if err = os.MkdirAll(pnpmBinDir, 0755); err != nil {
			return packit.BuildResult{}, fmt.Errorf("failed to create bin dir: %w", err)
		}

		shimContent := fmt.Sprintf("#!/bin/sh\nexec node %s \"$@\"\n", entryPoint)
		pnpmBinPath := filepath.Join(pnpmBinDir, "pnpm")
		if err = os.WriteFile(pnpmBinPath, []byte(shimContent), 0755); err != nil {
			return packit.BuildResult{}, fmt.Errorf("failed to write pnpm shim: %w", err)
		}
		// ──────────────────────────────────────────────────────────────────────

		logger.Action("Completed in %s", duration.Round(time.Millisecond))
		logger.Break()

		sbomDisabled, err := checkSbomDisabled()
		if err != nil {
			return packit.BuildResult{}, err
		}

		if sbomDisabled {
			logger.Subprocess("Skipping SBOM generation for pnpm")
			logger.Break()
		} else {
			logger.GeneratingSBOM(pnpmLayer.Path)
			var sbomContent sbom.SBOM
			duration, err = clock.Measure(func() error {
				sbomContent, err = sbomGenerator.GenerateFromDependency(dependency, pnpmLayer.Path)
				return err
			})
			if err != nil {
				return packit.BuildResult{}, err
			}

			logger.Action("Completed in %s", duration.Round(time.Millisecond))
			logger.Break()

			logger.FormattingSBOM(context.BuildpackInfo.SBOMFormats...)
			pnpmLayer.SBOM, err = sbomContent.InFormats(context.BuildpackInfo.SBOMFormats...)
			if err != nil {
				return packit.BuildResult{}, err
			}
		}

		pnpmLayer.Metadata = map[string]interface{}{
			DependencyCacheKey: dependency.Checksum,
		}

		return packit.BuildResult{
			Layers: []packit.Layer{pnpmLayer},
			Build:  buildMetadata,
			Launch: launchMetadata,
		}, nil
	}
}

func checkSbomDisabled() (bool, error) {
	if disableStr, ok := os.LookupEnv("BP_DISABLE_SBOM"); ok {
		disable, err := strconv.ParseBool(disableStr)
		if err != nil {
			return false, fmt.Errorf("failed to parse BP_DISABLE_SBOM value %s: %w", disableStr, err)
		}
		return disable, nil
	}
	return false, nil
}
