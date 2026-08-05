package pnpm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/paketo-buildpacks/packit/v2"
	"github.com/paketo-buildpacks/packit/v2/chronos"
	"github.com/paketo-buildpacks/packit/v2/draft"
	"github.com/paketo-buildpacks/packit/v2/postal"
	"github.com/paketo-buildpacks/packit/v2/sbom"
	"github.com/paketo-buildpacks/packit/v2/scribe"
)

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

// packageJSON is a minimal representation of the pnpm package.json used only
// to resolve the `bin` field after tarball extraction.
type packageJSON struct {
	// Bin is the map of binary name → relative path declared in package.json.
	// pnpm uses the map form, e.g. {"pnpm": "dist/pnpm.cjs"}.
	Bin map[string]string `json:"bin"`
}

// parsePnpmEntryPoint reads <layerPath>/package.json and returns the absolute
// path to the pnpm entry-point declared under `bin["pnpm"]`.
//
// Returning an explicit, descriptive error here ensures that if the pnpm
// maintainers rename their entry file (e.g. pnpm.cjs → pnpm.mjs), the
// buildpack fails with a clear message instead of a cryptic "command not
// found" at container start-up.
func parsePnpmEntryPoint(layerPath string) (string, error) {
	pkgJSONPath := filepath.Join(layerPath, "package.json")

	data, err := os.ReadFile(pkgJSONPath)
	if err != nil {
		return "", fmt.Errorf("failed to read package.json from pnpm layer (%s): %w", pkgJSONPath, err)
	}

	var pkg packageJSON
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

	// relEntry is relative to the package root (e.g. "dist/pnpm.cjs").
	// Resolve it to an absolute path so the shim is unambiguous.
	return filepath.Join(layerPath, relEntry), nil
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
		if !ok {
			version = "default"
		}

		dependency, err := dependencyManager.Resolve(
			filepath.Join(context.CNBPath, "buildpack.toml"),
			PNPMDependency,
			version,
			context.Stack)
		if err != nil {
			return packit.BuildResult{}, err
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

		logger.Subprocess("Installing pnpm")

		duration, err := clock.Measure(func() error {
			return dependencyManager.Deliver(dependency, context.CNBPath, pnpmLayer.Path, context.Platform.Path)
		})
		if err != nil {
			return packit.BuildResult{}, err
		}

		// ── Dynamic shim creation ──────────────────────────────────────────────
		// Instead of hardcoding "pnpm.cjs", we read the `bin["pnpm"]` field from
		// the package.json that ships with the extracted tarball. This keeps the
		// buildpack resilient to upstream entry-point renames (e.g. pnpm.mjs).
		entryPoint, err := parsePnpmEntryPoint(pnpmLayer.Path)
		if err != nil {
			return packit.BuildResult{}, fmt.Errorf("could not determine pnpm entry point: %w", err)
		}

		pnpmBinDir := filepath.Join(pnpmLayer.Path, "bin")
		if err = os.MkdirAll(pnpmBinDir, 0755); err != nil {
			return packit.BuildResult{}, fmt.Errorf("failed to create bin dir: %w", err)
		}

		// The shim delegates to `node <entryPoint>` so the correct Node.js
		// runtime (provided by an earlier buildpack layer) is always used.
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