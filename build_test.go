package pnpm_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/paketo-buildpacks/packit/v2"
	"github.com/paketo-buildpacks/packit/v2/chronos"

	//nolint Ignore SA1019, informed usage of deprecated package
	"github.com/paketo-buildpacks/packit/v2/paketosbom"
	"github.com/paketo-buildpacks/packit/v2/postal"
	"github.com/paketo-buildpacks/packit/v2/sbom"
	"github.com/paketo-buildpacks/packit/v2/scribe"
	"github.com/paketo-buildpacks/pnpm"
	"github.com/paketo-buildpacks/pnpm/fakes"
	"github.com/sclevine/spec"

	. "github.com/onsi/gomega"
)

// testBuild defines the unit test suite for the pnpm Buildpack's Build phase.
func testBuild(t *testing.T, context spec.G, it spec.S) {
	var (
		Expect = NewWithT(t).Expect

		layersDir         string
		workingDir        string
		cnbDir            string
		dependencyManager *fakes.DependencyManager
		sbomGenerator     *fakes.SBOMGenerator

		buffer *bytes.Buffer

		buildContext packit.BuildContext
		build        packit.BuildFunc
	)

	it.Before(func() {
		var err error

		// Create temporary directories for layers, buildpack metadata (CNB), and application workspace
		layersDir, err = os.MkdirTemp("", "layers")
		Expect(err).NotTo(HaveOccurred())

		cnbDir, err = os.MkdirTemp("", "cnb")
		Expect(err).NotTo(HaveOccurred())

		workingDir, err = os.MkdirTemp("", "working-dir")
		Expect(err).NotTo(HaveOccurred())

		// Initialize fake dependency manager with expected mock return values
		dependencyManager = &fakes.DependencyManager{}
		dependencyManager.ResolveCall.Returns.Dependency = postal.Dependency{
			ID:       "pnpm",
			Name:     "pnpm-dependency-name",
			Checksum: "sha256:34e198cb1e43237517ecedfd31f9ae26a6c0a3e5366ce58a2d05f4b21fb5f19a",
			Stacks:   []string{"some-stack"},
			URI:      "pnpm-dependency-uri",
			Version:  "11.20.0",
		}
		dependencyManager.GenerateBillOfMaterialsCall.Returns.BOMEntrySlice = []packit.BOMEntry{
			{
				Name: "pnpm",
				Metadata: paketosbom.BOMMetadata{
					URI:     "pnpm-dependency-uri",
					Version: "11.20.0",
					Checksum: paketosbom.BOMChecksum{
						Algorithm: paketosbom.SHA256,
						Hash:      "34e198cb1e43237517ecedfd31f9ae26a6c0a3e5366ce58a2d05f4b21fb5f19a",
					},
				},
			},
		}

		// Stub Deliver call to simulate pnpm package extraction by creating layer package.json
		dependencyManager.DeliverCall.Stub = func(_ postal.Dependency, _, layerPath, _ string) error {
			pkgJSON := `{"name":"pnpm","version":"test","bin":{"pnpm":"dist/pnpm.cjs"}}`
			return os.WriteFile(filepath.Join(layerPath, "package.json"), []byte(pkgJSON), 0644)
		}

		// Mock SBOM generator
		sbomGenerator = &fakes.SBOMGenerator{}
		sbomGenerator.GenerateFromDependencyCall.Returns.SBOM = sbom.SBOM{}

		buffer = bytes.NewBuffer(nil)

		// Set up standard build context
		buildContext = packit.BuildContext{
			WorkingDir: workingDir,
			CNBPath:    cnbDir,
			Stack:      "some-stack",
			BuildpackInfo: packit.BuildpackInfo{
				Name:        "Some Buildpack",
				Version:     "some-version",
				SBOMFormats: []string{sbom.CycloneDXFormat, sbom.SPDXFormat},
			},
			Plan: packit.BuildpackPlan{
				Entries: []packit.BuildpackPlanEntry{
					{
						Name: "pnpm",
					},
				},
			},
			Platform: packit.Platform{Path: "platform"},
			Layers:   packit.Layers{Path: layersDir},
		}

		// Instantiate the Build function under test
		build = pnpm.Build(dependencyManager,
			sbomGenerator,
			chronos.DefaultClock,
			scribe.NewEmitter(buffer))
	})

	it.After(func() {
		// Clean up temporary workspace directories
		Expect(os.RemoveAll(layersDir)).To(Succeed())
		Expect(os.RemoveAll(cnbDir)).To(Succeed())
		Expect(os.RemoveAll(workingDir)).To(Succeed())
	})

	// ── Scenario 1: Successful Build and Installation ───────────────────────────
	it("returns a result that installs pnpm and writes executable shim", func() {
		result, err := build(buildContext)
		Expect(err).NotTo(HaveOccurred())

		// Assert layer result attributes
		Expect(result.Layers).To(HaveLen(1))
		layer := result.Layers[0]

		Expect(layer.Name).To(Equal("pnpm"))
		Expect(layer.Path).To(Equal(filepath.Join(layersDir, "pnpm")))
		Expect(layer.Metadata).To(HaveKey("dependency-sha"))

		// Assert SBOM formats generated
		Expect(layer.SBOM.Formats()).To(HaveLen(2))

		cdx := layer.SBOM.Formats()[0]
		spdx := layer.SBOM.Formats()[1]

		Expect(cdx.Extension).To(Equal("cdx.json"))

		content, err := io.ReadAll(cdx.Content)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(MatchJSON(`{
			"$schema": "http://cyclonedx.org/schema/bom-1.3.schema.json",
			"bomFormat": "CycloneDX",
			"metadata": {
				"tools": [
					{
						"name": "",
						"vendor": "anchore"
					}
				]
			},
			"specVersion": "1.3",
			"version": 1
		}`))

		Expect(spdx.Extension).To(Equal("spdx.json"))
		content, err = io.ReadAll(spdx.Content)
		Expect(err).NotTo(HaveOccurred())

		versionPattern := regexp.MustCompile(`"licenseListVersion": "\d+\.\d+"`)
		contentReplaced := versionPattern.ReplaceAllString(string(content), `"licenseListVersion": "x.x"`)

		uuidRegex := regexp.MustCompile(`[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}`)
		contentReplaced = uuidRegex.ReplaceAllString(contentReplaced, "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx")

		Expect(string(contentReplaced)).To(MatchJSON(`{
			"SPDXID": "SPDXRef-DOCUMENT",
			"creationInfo": {
				"created": "0001-01-01T00:00:00Z",
				"creators": [
					"Organization: Anchore, Inc",
					"Tool: -"
				],
				"licenseListVersion": "x.x"
			},
			"dataLicense": "CC0-1.0",
            "documentNamespace": "https://paketo.io/unknown-source-type/unknown-xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
			"name": "unknown",
			"packages": [
				{
					"SPDXID": "SPDXRef-DocumentRoot-Unknown-",
					"copyrightText": "NOASSERTION",
					"downloadLocation": "NOASSERTION",
					"filesAnalyzed": false,
					"licenseConcluded": "NOASSERTION",
					"licenseDeclared": "NOASSERTION",
					"name": "",
					"supplier": "NOASSERTION"
				}
			],
			"relationships": [
				{
					"relatedSpdxElement": "SPDXRef-DocumentRoot-Unknown-",
					"relationshipType": "DESCRIBES",
					"spdxElementId": "SPDXRef-DOCUMENT"
				}
			],
			"spdxVersion": "SPDX-2.2"
		}`))

		// Assert executable bin/pnpm shim creation
		shimPath := filepath.Join(layersDir, "pnpm", "bin", "pnpm")
		shimBytes, err := os.ReadFile(shimPath)
		Expect(err).NotTo(HaveOccurred())
		expectedTarget := filepath.Join(layersDir, "pnpm", "dist", "pnpm.cjs")
		Expect(string(shimBytes)).To(Equal("#!/bin/sh\nexec node " + expectedTarget + " \"$@\"\n"))

		// Assert logger output
		Expect(buffer.String()).To(ContainSubstring("Some Buildpack some-version"))
		Expect(buffer.String()).To(ContainSubstring("Executing build process"))
		Expect(buffer.String()).To(ContainSubstring("Installing pnpm"))
	})

	// ── Scenario 2: Build & Launch Layer Availability Flags ────────────────────
	context("when the plan entry requires the dependency during build and launch phases", func() {
		it.Before(func() {
			buildContext.Plan.Entries[0].Metadata = map[string]interface{}{
				"build":  true,
				"launch": true,
			}
		})

		it("makes the layer available in both build and launch phases", func() {
			result, err := build(buildContext)
			Expect(err).NotTo(HaveOccurred())

			Expect(result.Layers).To(HaveLen(1))
			layer := result.Layers[0]

			Expect(layer.Name).To(Equal("pnpm"))
			Expect(layer.Path).To(Equal(filepath.Join(layersDir, "pnpm")))
			Expect(layer.Build).To(BeTrue())
			Expect(layer.Launch).To(BeTrue())
			Expect(layer.Cache).To(BeTrue())
			Expect(layer.Metadata).To(HaveKey("dependency-sha"))
		})
	})

	// ── Failure Scenarios ────────────────────────────────────────────────────────
	context("failure cases", func() {
		context("when the pnpm layer metadata cannot be parsed", func() {
			it.Before(func() {
				// Corrupt layer metadata file
				err := os.WriteFile(filepath.Join(layersDir, "pnpm.toml"), nil, 0000)
				Expect(err).NotTo(HaveOccurred())
			})

			it("returns an error", func() {
				_, err := build(buildContext)
				Expect(err).To(MatchError(ContainSubstring("failed to parse layer content metadata")))
			})
		})

		context("when the dependency cannot be resolved", func() {
			it.Before(func() {
				// Pass an unresolvable version constraint
				buildContext.Plan.Entries[0].Metadata = map[string]interface{}{
					"version": "unresolvable-invalid-version-xyz",
				}
				dependencyManager.ResolveCall.Returns.Error = errors.New("failed to resolve dependency")
			})

			it("returns a resolution error", func() {
				_, err := build(buildContext)
				Expect(err).To(MatchError(ContainSubstring("failed to resolve dependency")))
			})
		})

		context("when the layers directory cannot be written to", func() {
			it.Before(func() {
				// Remove write permissions on layer directory
				Expect(os.Chmod(layersDir, 4444)).To(Succeed())
			})

			it.After(func() {
				Expect(os.Chmod(layersDir, os.ModePerm)).To(Succeed())
			})

			it("returns a permission denied error", func() {
				_, err := build(buildContext)
				Expect(err).To(MatchError(ContainSubstring("permission denied")))
			})
		})

		context("when the dependency delivery fails", func() {
			it.Before(func() {
				dependencyManager.DeliverCall.Stub = nil
				dependencyManager.DeliverCall.Returns.Error = errors.New("failed to install dependency")
			})

			it("returns an installation error", func() {
				_, err := build(buildContext)
				Expect(err).To(MatchError("failed to install dependency"))
			})
		})

		context("when package.json is missing from layer after delivery", func() {
			it.Before(func() {
				// Stub deliver to succeed without creating package.json
				dependencyManager.DeliverCall.Stub = func(_ postal.Dependency, _, _ string, _ string) error {
					return nil
				}
			})

			it("returns a descriptive missing package.json error", func() {
				_, err := build(buildContext)
				Expect(err).To(MatchError(ContainSubstring("could not determine pnpm entry point")))
				Expect(err).To(MatchError(ContainSubstring("package.json")))
			})
		})

		context("when package.json lacks a bin.pnpm entry", func() {
			it.Before(func() {
				dependencyManager.DeliverCall.Stub = func(_ postal.Dependency, _, layerPath string, _ string) error {
					// Write invalid package.json without bin.pnpm key
					return os.WriteFile(
						filepath.Join(layerPath, "package.json"),
						[]byte(`{"name":"pnpm","bin":{"other":"dist/other.cjs"}}`),
						0644,
					)
				}
			})

			it("returns a descriptive entry point error", func() {
				_, err := build(buildContext)
				Expect(err).To(MatchError(ContainSubstring("could not determine pnpm entry point")))
				Expect(err).To(MatchError(ContainSubstring("'pnpm' entry under 'bin'")))
			})
		})

		context("when generating the SBOM returns an error", func() {
			it.Before(func() {
				buildContext.BuildpackInfo = packit.BuildpackInfo{SBOMFormats: []string{"random-format"}}
			})

			it("returns an unsupported SBOM format error", func() {
				_, err := build(buildContext)
				Expect(err).To(MatchError("unsupported SBOM format: 'random-format'"))
			})
		})

		context("when formatting the SBOM returns an error", func() {
			it.Before(func() {
				sbomGenerator.GenerateFromDependencyCall.Returns.Error = errors.New("failed to generate SBOM")
			})

			it("returns an SBOM generation error", func() {
				_, err := build(buildContext)
				Expect(err).To(MatchError(ContainSubstring("failed to generate SBOM")))
			})
		})

		context("when BP_DISABLE_SBOM is set to an invalid boolean value", func() {
			it.Before(func() {
				Expect(os.Setenv("BP_DISABLE_SBOM", "not-a-bool")).To(Succeed())
			})

			it.After(func() {
				Expect(os.Unsetenv("BP_DISABLE_SBOM")).To(Succeed())
			})

			it("returns a parse error for BP_DISABLE_SBOM", func() {
				_, err := build(buildContext)
				Expect(err).To(MatchError(ContainSubstring("failed to parse BP_DISABLE_SBOM")))
			})
		})
	})
}