package pnpm_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/paketo-buildpacks/packit/v2"
	"github.com/paketo-buildpacks/pnpm"
	"github.com/sclevine/spec"

	. "github.com/onsi/gomega"
)

// testDetect defines the unit test suite for the pnpm Buildpack's Detect phase.
func testDetect(t *testing.T, context spec.G, it spec.S) {
	var (
		Expect = NewWithT(t).Expect

		workingDir string
		detect     packit.DetectFunc
	)

	it.Before(func() {
		var err error

		// Create an isolated temporary directory representing the application working directory
		workingDir, err = os.MkdirTemp("", "working-dir")
		Expect(err).NotTo(HaveOccurred())

		// Initialize the buildpack Detect function
		detect = pnpm.Detect()
	})

	it.After(func() {
		// Clean up the temporary workspace directory after each test
		Expect(os.RemoveAll(workingDir)).To(Succeed())
	})

	// ── Scenario 1: Default Fallback Detection ───────────────────────────────────
	it("provides pnpm and requires the default dynamic version when no configuration files exist", func() {
		result, err := detect(packit.DetectContext{
			WorkingDir: workingDir,
		})
		Expect(err).NotTo(HaveOccurred())

		// Assert that the buildplan provides "pnpm"
		Expect(result.Plan.Provides).To(Equal([]packit.BuildPlanProvision{
			{Name: "pnpm"},
		}))

		// Assert that a fallback requirement is added to the plan with "default" source
		Expect(result.Plan.Requires).To(HaveLen(1))
		Expect(result.Plan.Requires[0].Name).To(Equal("pnpm"))

		metadata, ok := result.Plan.Requires[0].Metadata.(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(metadata["version-source"]).To(Equal("default"))
	})

	// ── Scenario 2: Environment Variable Override Priority ───────────────────────
	context("when BP_PNPM_VERSION environment variable is set", func() {
		it.Before(func() {
			// Set explicit environment variable override
			Expect(os.Setenv("BP_PNPM_VERSION", "10.34.5")).To(Succeed())
		})

		it.After(func() {
			// Clean up environment variable after test
			Expect(os.Unsetenv("BP_PNPM_VERSION")).To(Succeed())
		})

		it("provides pnpm and requires the version specified by BP_PNPM_VERSION", func() {
			result, err := detect(packit.DetectContext{
				WorkingDir: workingDir,
			})
			Expect(err).NotTo(HaveOccurred())

			// Assert buildplan provision
			Expect(result.Plan.Provides).To(Equal([]packit.BuildPlanProvision{
				{Name: "pnpm"},
			}))

			// Assert requirement metadata reflects the environment variable source
			Expect(result.Plan.Requires).To(HaveLen(1))
			Expect(result.Plan.Requires[0].Name).To(Equal("pnpm"))

			metadata, ok := result.Plan.Requires[0].Metadata.(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(metadata["version"]).To(Equal("10.34.5"))
			Expect(metadata["version-source"]).To(Equal("BP_PNPM_VERSION"))
		})
	})

	// ── Scenario 3: package.json packageManager Configuration ───────────────────
	context("when package.json declares a packageManager version constraint", func() {
		it.Before(func() {
			// Create a mock package.json file declaring pnpm version 11.20.0
			pkgJSON := `{"packageManager": "pnpm@11.20.0"}`
			Expect(os.WriteFile(filepath.Join(workingDir, "package.json"), []byte(pkgJSON), 0644)).To(Succeed())
		})

		it("provides pnpm and requires the version parsed from package.json", func() {
			result, err := detect(packit.DetectContext{
				WorkingDir: workingDir,
			})
			Expect(err).NotTo(HaveOccurred())

			// Assert requirement metadata reflects package.json as the version source
			Expect(result.Plan.Provides).To(Equal([]packit.BuildPlanProvision{
				{Name: "pnpm"},
			}))
			Expect(result.Plan.Requires).To(HaveLen(1))
			Expect(result.Plan.Requires[0].Name).To(Equal("pnpm"))

			metadata, ok := result.Plan.Requires[0].Metadata.(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(metadata["version"]).To(Equal("11.20.0"))
			Expect(metadata["version-source"]).To(Equal("package.json"))
		})
	})

	// ── Scenario 4: pnpm-lock.yaml Lockfile Version Auto-Detection ───────────────
	context("when pnpm-lock.yaml is present in working directory", func() {
		it.Before(func() {
			// Create a pnpm-lock.yaml file specifying lockfileVersion 10.0 (maps to PNPM v10)
			lockfileContent := "lockfileVersion: '10.0'\n"
			Expect(os.WriteFile(filepath.Join(workingDir, "pnpm-lock.yaml"), []byte(lockfileContent), 0644)).To(Succeed())
		})

		it("provides pnpm and infers requirement from lockfileVersion", func() {
			result, err := detect(packit.DetectContext{
				WorkingDir: workingDir,
			})
			Expect(err).NotTo(HaveOccurred())

			// Assert requirement metadata reflects lockfile as the version source
			Expect(result.Plan.Provides).To(Equal([]packit.BuildPlanProvision{
				{Name: "pnpm"},
			}))
			Expect(result.Plan.Requires).To(HaveLen(1))
			Expect(result.Plan.Requires[0].Name).To(Equal("pnpm"))

			metadata, ok := result.Plan.Requires[0].Metadata.(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(metadata["version-source"]).To(Equal("pnpm-lock.yaml"))
		})
	})
}