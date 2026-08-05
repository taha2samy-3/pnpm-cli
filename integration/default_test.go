package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/paketo-buildpacks/occam"
	"github.com/sclevine/spec"

	. "github.com/onsi/gomega"
	. "github.com/paketo-buildpacks/occam/matchers"
)

func testDefault(t *testing.T, context spec.G, it spec.S) {
	var (
		Expect     = NewWithT(t).Expect
		Eventually = NewWithT(t).Eventually

		pack   occam.Pack
		docker occam.Docker

		pullPolicy       = "never"
		extenderBuildStr = ""
	)

	it.Before(func() {
		pack = occam.NewPack().WithVerbose()
		docker = occam.NewDocker()

		if settings.Extensions.UbiNodejsExtension.Online != "" {
			pullPolicy = "always"
			extenderBuildStr = "[extender (build)] "
		}
	})

	context("when the buildpack is run with pack build", func() {
		var (
			image occam.Image

			name    string
			source  string
			sbomDir string

			container occam.Container
		)

		it.Before(func() {
			var err error
			name, err = occam.RandomName()
			Expect(err).NotTo(HaveOccurred())

			source, err = occam.Source(filepath.Join("testdata", "default_app"))
			Expect(err).NotTo(HaveOccurred())

			sbomDir, err = os.MkdirTemp("", "sbom")
			Expect(err).NotTo(HaveOccurred())
			Expect(os.Chmod(sbomDir, os.ModePerm)).To(Succeed())
		})

		it.After(func() {
			if container.ID != "" {
				Expect(docker.Container.Remove.Execute(container.ID)).To(Succeed())
			}
			if image.ID != "" {
				Expect(docker.Image.Remove.Execute(image.ID)).To(Succeed())
			}
			Expect(docker.Volume.Remove.Execute(occam.CacheVolumeNames(name))).To(Succeed())
			Expect(os.RemoveAll(source)).To(Succeed())
			Expect(os.RemoveAll(sbomDir)).To(Succeed())
		})

		it("builds with the defaults", func() {
			var (
				logs fmt.Stringer
				err  error
			)

			image, logs, err = pack.WithNoColor().Build.
				WithExtensions(
					settings.Extensions.UbiNodejsExtension.Online,
				).
				WithBuildpacks(
					settings.Buildpacks.PNPM.Online,
					settings.Buildpacks.BuildPlan.Online,
				).
				WithSBOMOutputDir(sbomDir).
				WithEnv(map[string]string{"BP_LOG_LEVEL": "DEBUG"}).
				WithPullPolicy(pullPolicy).
				Execute(name, source)
			Expect(err).ToNot(HaveOccurred(), logs.String)

			// Ensure pnpm is installed correctly and present on PATH inside container
			container, err = docker.Container.Run.WithCommand("command -v pnpm").Execute(image.ID)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() string {
				cLogs, err := docker.Container.Logs.Execute(container.ID)
				Expect(err).NotTo(HaveOccurred())
				return cLogs.String()
			}).Should(ContainSubstring("pnpm"))

			// QuoteMeta escapes special regex characters in extenderBuildStr (e.g. "[" and "]")
			escapedExtender := regexp.QuoteMeta(extenderBuildStr)

			Expect(logs).To(ContainLines(
				MatchRegexp(fmt.Sprintf(`%s%s \d+\.\d+\.\d+`, escapedExtender, settings.Buildpack.Name)),
				extenderBuildStr+"  Executing build process",
				MatchRegexp(fmt.Sprintf(`%s    Installing pnpm`, escapedExtender)),
				MatchRegexp(fmt.Sprintf(`%s      Completed in ([0-9]*(\.[0-9]*)?[a-z]+)+`, escapedExtender)),
				extenderBuildStr,
				fmt.Sprintf("%s  Generating SBOM for /layers/%s/pnpm", extenderBuildStr, strings.ReplaceAll(settings.Buildpack.ID, "/", "_")),
				MatchRegexp(fmt.Sprintf(`%s      Completed in \d+(\.?\d+)*`, escapedExtender)),
				extenderBuildStr,
				extenderBuildStr+"  Writing SBOM in the following format(s):",
				extenderBuildStr+"    application/vnd.cyclonedx+json",
				extenderBuildStr+"    application/spdx+json",
				extenderBuildStr+"    application/vnd.syft+json",
				extenderBuildStr,
			))

			// check that all required SBOM files are present
			Expect(filepath.Join(sbomDir, "sbom", "launch", strings.ReplaceAll(settings.Buildpack.ID, "/", "_"), "pnpm", "sbom.cdx.json")).To(BeARegularFile())
			Expect(filepath.Join(sbomDir, "sbom", "launch", strings.ReplaceAll(settings.Buildpack.ID, "/", "_"), "pnpm", "sbom.spdx.json")).To(BeARegularFile())
			Expect(filepath.Join(sbomDir, "sbom", "launch", strings.ReplaceAll(settings.Buildpack.ID, "/", "_"), "pnpm", "sbom.syft.json")).To(BeARegularFile())

			// check an SBOM file to make sure it has an entry for pnpm
			contents, err := os.ReadFile(filepath.Join(sbomDir, "sbom", "launch", strings.ReplaceAll(settings.Buildpack.ID, "/", "_"), "pnpm", "sbom.cdx.json"))
			Expect(err).NotTo(HaveOccurred())

			Expect(string(contents)).To(ContainSubstring(`"name": "pnpm"`))
		})
	})

	context("BP_DISABLE_SBOM is set to true", func() {
		var (
			image occam.Image

			name    string
			source  string
			sbomDir string

			container occam.Container
		)

		it.Before(func() {
			var err error
			name, err = occam.RandomName()
			Expect(err).NotTo(HaveOccurred())

			source, err = occam.Source(filepath.Join("testdata", "default_app"))
			Expect(err).NotTo(HaveOccurred())

			sbomDir, err = os.MkdirTemp("", "sbom")
			Expect(err).NotTo(HaveOccurred())
			Expect(os.Chmod(sbomDir, os.ModePerm)).To(Succeed())
		})

		it.After(func() {
			if container.ID != "" {
				Expect(docker.Container.Remove.Execute(container.ID)).To(Succeed())
			}
			if image.ID != "" {
				Expect(docker.Image.Remove.Execute(image.ID)).To(Succeed())
			}
			Expect(docker.Volume.Remove.Execute(occam.CacheVolumeNames(name))).To(Succeed())
			Expect(os.RemoveAll(source)).To(Succeed())
			Expect(os.RemoveAll(sbomDir)).To(Succeed())
		})

		it("skips SBOM generation", func() {
			var (
				logs fmt.Stringer
				err  error
			)

			image, logs, err = pack.WithNoColor().Build.
				WithExtensions(
					settings.Extensions.UbiNodejsExtension.Online,
				).
				WithPullPolicy(pullPolicy).
				WithBuildpacks(
					settings.Buildpacks.PNPM.Online,
					settings.Buildpacks.BuildPlan.Online).
				WithSBOMOutputDir(sbomDir).
				WithEnv(map[string]string{
					"BP_LOG_LEVEL":    "DEBUG",
					"BP_DISABLE_SBOM": "true",
				}).
				Execute(name, source)
			Expect(err).ToNot(HaveOccurred(), logs.String)

			// Ensure pnpm is installed correctly
			container, err = docker.Container.Run.WithCommand("command -v pnpm").Execute(image.ID)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() string {
				cLogs, err := docker.Container.Logs.Execute(container.ID)
				Expect(err).NotTo(HaveOccurred())
				return cLogs.String()
			}).Should(ContainSubstring("pnpm"))

			escapedExtender := regexp.QuoteMeta(extenderBuildStr)

			Expect(logs).To(ContainLines(
				MatchRegexp(fmt.Sprintf(`%s%s \d+\.\d+\.\d+`, escapedExtender, settings.Buildpack.Name)),
				extenderBuildStr+"  Executing build process",
				MatchRegexp(fmt.Sprintf(`%s    Installing pnpm`, escapedExtender)),
				MatchRegexp(fmt.Sprintf(`%s      Completed in ([0-9]*(\.[0-9]*)?[a-z]+)+`, escapedExtender)),
				extenderBuildStr,
				extenderBuildStr+"    Skipping SBOM generation for pnpm",
			))

			// check that SBOM files were not generated
			Expect(filepath.Join(sbomDir, "sbom", "launch", strings.ReplaceAll(settings.Buildpack.ID, "/", "_"), "pnpm", "sbom.cdx.json")).ToNot(BeARegularFile())
			Expect(filepath.Join(sbomDir, "sbom", "launch", strings.ReplaceAll(settings.Buildpack.ID, "/", "_"), "pnpm", "sbom.spdx.json")).ToNot(BeARegularFile())
			Expect(filepath.Join(sbomDir, "sbom", "launch", strings.ReplaceAll(settings.Buildpack.ID, "/", "_"), "pnpm", "sbom.syft.json")).ToNot(BeARegularFile())
		})
	})
}