// Package main is the pnpm dependency retrieval script.
//
// It queries the official npm registry for pnpm package metadata, downloads
// each release tarball to compute its SHA-256 checksum, and writes a JSON
// array of cargo.ConfigMetadataDependency structs to the output file specified
// on the command line.
//
// Usage (driven by scripts/retrieve.sh or the dependency/Makefile):
//
//	retrieval <buildpack.toml-path> <output-file>
package main

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Masterminds/semver/v3"
	buildpackConfig "github.com/paketo-buildpacks/libdependency/buildpack_config"
	"github.com/paketo-buildpacks/libdependency/retrieve"
	"github.com/paketo-buildpacks/libdependency/versionology"
	"github.com/paketo-buildpacks/packit/v2/cargo"
	"github.com/paketo-buildpacks/packit/v2/fs"
)

// pnpmDependencyID is the canonical ID used throughout the Paketo pnpm buildpack.
const pnpmDependencyID = "pnpm"

// npmRegistryURL is the full-metadata endpoint for pnpm on the npm registry.
// Fetching this URL returns a JSON document containing every published version.
const npmRegistryURL = "https://registry.npmjs.org/pnpm"

// ── npm registry response types ──────────────────────────────────────────────

// npmPackageMetadata is the top-level response from the npm registry for a
// given package (i.e. https://registry.npmjs.org/<package>).
type npmPackageMetadata struct {
	// Versions maps each published version string to its full version metadata.
	Versions map[string]npmVersionMetadata `json:"versions"`
}

// npmVersionMetadata holds the per-version data we care about.
type npmVersionMetadata struct {
	Version string `json:"version"`
	License string `json:"license"`
	Dist    struct {
		Tarball string `json:"tarball"` // download URL for the .tgz
		Shasum  string `json:"shasum"`  // SHA-1 hex (npm's canonical integrity value)
	} `json:"dist"`
}

// ── versionology adapter ──────────────────────────────────────────────────────

// pnpmVersionFetcher wraps a semver.Version and satisfies the
// versionology.VersionFetcher interface so it can be passed to
// retrieve.GetNewVersionsForId.
type pnpmVersionFetcher struct {
	semverVersion *semver.Version
}

func (p pnpmVersionFetcher) Version() *semver.Version {
	return p.semverVersion
}

// ── entry point ───────────────────────────────────────────────────────────────

func main() {
	buildpackTomlPath, output := retrieve.FetchArgs()
	validate(buildpackTomlPath, output)

	// Parse the buildpack.toml to discover configured targets (os/arch pairs).
	config, err := buildpackConfig.ParseBuildpackToml(buildpackTomlPath)
	if err != nil {
		panic(fmt.Errorf("could not parse buildpack.toml: %w", err))
	}

	// Default to linux/amd64 when no explicit targets are configured.
	if len(config.Targets) == 0 {
		config.Targets = []cargo.ConfigTarget{{OS: "linux", Arch: "amd64"}}
	}

	// Fetch every published pnpm version from the npm registry.
	allNpmVersions, err := getAllNpmVersions()
	if err != nil {
		panic(fmt.Errorf("could not fetch pnpm versions from npm: %w", err))
	}

	// Ask libdependency which versions are genuinely new (not yet in buildpack.toml).
	newVersions, err := retrieve.GetNewVersionsForId(
		pnpmDependencyID,
		config,
		func() (versionology.VersionFetcherArray, error) { return allNpmVersions, nil },
	)
	if err != nil {
		panic(fmt.Errorf("could not determine new pnpm versions: %w", err))
	}

	// Build a ConfigMetadataDependency for every (new version × target) pair.
	var allDependencies []versionology.Dependency

	for _, target := range config.Targets {
		platform := retrieve.Platform{OS: target.OS, Arch: target.Arch}
		deps := retrieve.GenerateAllMetadataWithPlatform(newVersions, generatePnpmMetadata, platform)
		allDependencies = append(allDependencies, deps...)
	}

	// Serialise and write the result.
	metadataJSON, err := json.Marshal(allDependencies)
	if err != nil {
		panic(fmt.Errorf("unable to marshal dependency metadata to JSON: %w", err))
	}
	if err = os.WriteFile(output, metadataJSON, os.ModePerm); err != nil {
		panic(fmt.Errorf("cannot write output to %s: %w", output, err))
	}

	fmt.Printf("Wrote %d dependencies to %s\n", len(allDependencies), output)
}

// ── version enumeration ───────────────────────────────────────────────────────

// getAllNpmVersions fetches the full pnpm metadata from the npm registry and
// returns a VersionFetcherArray containing every published semver version.
func getAllNpmVersions() (versionology.VersionFetcherArray, error) {
	webClient := NewWebClient()

	body, err := webClient.Get(npmRegistryURL)
	if err != nil {
		return nil, fmt.Errorf("could not GET %s: %w", npmRegistryURL, err)
	}

	var pkgMeta npmPackageMetadata
	if err := json.Unmarshal(body, &pkgMeta); err != nil {
		return nil, fmt.Errorf("could not unmarshal npm registry response: %w", err)
	}

	var versions versionology.VersionFetcherArray
	for versionStr := range pkgMeta.Versions {
		sv, err := semver.NewVersion(versionStr)
		if err != nil {
			// Skip non-semver tags (e.g. pre-release labels that do not parse cleanly).
			continue
		}
		versions = append(versions, pnpmVersionFetcher{semverVersion: sv})
	}

	return versions, nil
}

// ── metadata generation ───────────────────────────────────────────────────────

// generatePnpmMetadata is the retrieve.GenerateMetadataWithPlatformFunc called
// by retrieve.GenerateAllMetadataWithPlatform for each (version, platform) pair.
//
// It:
//  1. Fetches the specific version metadata from the npm registry.
//  2. Downloads the tarball to a temp file.
//  3. Verifies the SHA-1 checksum against npm's `dist.shasum`.
//  4. Computes the SHA-256 checksum used by Paketo/packit.
//  5. Builds and returns a cargo.ConfigMetadataDependency.
func generatePnpmMetadata(
	versionFetcher versionology.VersionFetcher,
	platform retrieve.Platform,
) ([]versionology.Dependency, error) {
	version := versionFetcher.Version().String()
	webClient := NewWebClient()

	// ── 1. Fetch per-version metadata from the npm registry ──────────────────
	versionMetaURL := fmt.Sprintf("%s/%s", npmRegistryURL, version)
	body, err := webClient.Get(versionMetaURL)
	if err != nil {
		return nil, fmt.Errorf("could not fetch npm metadata for pnpm@%s: %w", version, err)
	}

	var meta npmVersionMetadata
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("could not unmarshal npm metadata for pnpm@%s: %w", version, err)
	}

	if meta.Dist.Tarball == "" {
		return nil, fmt.Errorf("npm registry returned no tarball URL for pnpm@%s", version)
	}
	if meta.Dist.Shasum == "" {
		return nil, fmt.Errorf("npm registry returned no shasum for pnpm@%s", version)
	}
	license := meta.License
	if license == "" {
		license = "MIT" // pnpm has always been MIT; use as a safe fallback
	}

	// ── 2. Download the tarball ───────────────────────────────────────────────
	tmpDir, err := os.MkdirTemp("", "pnpm-retrieve")
	if err != nil {
		return nil, fmt.Errorf("could not create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tgzPath := filepath.Join(tmpDir, fmt.Sprintf("pnpm-%s.tgz", version))
	if err = webClient.Download(meta.Dist.Tarball, tgzPath); err != nil {
		return nil, fmt.Errorf("could not download pnpm@%s tarball: %w", version, err)
	}

	// ── 3. Verify SHA-1 integrity ─────────────────────────────────────────────
	actualSHA1, err := fileSHA1(tgzPath)
	if err != nil {
		return nil, fmt.Errorf("could not compute SHA-1 for pnpm@%s: %w", version, err)
	}
	if actualSHA1 != meta.Dist.Shasum {
		return nil, fmt.Errorf(
			"SHA-1 mismatch for pnpm@%s: expected %s, got %s",
			version, meta.Dist.Shasum, actualSHA1,
		)
	}

	// ── 4. Compute SHA-256 (Paketo's preferred checksum algorithm) ────────────
	sha256Hex, err := fileSHA256(tgzPath)
	if err != nil {
		return nil, fmt.Errorf("could not compute SHA-256 for pnpm@%s: %w", version, err)
	}

	// ── 5. Build the dependency struct ────────────────────────────────────────
	dep := cargo.ConfigMetadataDependency{
		Arch:            platform.Arch,
		CPE:             fmt.Sprintf("cpe:2.3:a:pnpm:pnpm:%s:*:*:*:*:*:*:*", version),
		Checksum:        fmt.Sprintf("sha256:%s", sha256Hex),
		DeprecationDate: nil,
		ID:              pnpmDependencyID,
		Licenses:        []interface{}{license},
		Name:            "pnpm",
		OS:              platform.OS,
		PURL:            retrieve.GeneratePURL(pnpmDependencyID, version, sha256Hex, meta.Dist.Tarball),
		Source:          meta.Dist.Tarball,
		SourceChecksum:  fmt.Sprintf("sha256:%s", sha256Hex),
		StripComponents: 1,
		Stacks:          []string{"io.buildpacks.stacks.bionic", "io.buildpacks.stacks.jammy", "*"},
		URI:             meta.Dist.Tarball,
		Version:         version,
	}

	return []versionology.Dependency{{
		ConfigMetadataDependency: dep,
		SemverVersion:            versionFetcher.Version(),
	}}, nil
}

// ── file integrity helpers ────────────────────────────────────────────────────

// fileSHA256 computes the hex-encoded SHA-256 checksum of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to open file for SHA-256: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", fmt.Errorf("failed to hash file for SHA-256: %w", err)
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// fileSHA1 computes the hex-encoded SHA-1 checksum of the file at path.
// npm uses SHA-1 in its `dist.shasum` field; we verify against it before
// computing the SHA-256 checksum that Paketo stores in buildpack.toml.
func fileSHA1(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to open file for SHA-1: %w", err)
	}
	defer f.Close()

	h := sha1.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", fmt.Errorf("failed to hash file for SHA-1: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// ── validation ────────────────────────────────────────────────────────────────

// validate mirrors the check in libdependency/retrieve so callers get a clear
// error if the arguments are wrong before any network I/O takes place.
func validate(buildpackTomlPath, metadataFile string) {
	if exists, err := fs.Exists(buildpackTomlPath); err != nil {
		panic(err)
	} else if !exists {
		panic(fmt.Errorf("could not locate buildpack.toml at '%s'", buildpackTomlPath))
	}

	if metadataFile == "" {
		panic("metadataFile is required")
	}
}
