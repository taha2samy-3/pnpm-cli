package pnpm

import "github.com/paketo-buildpacks/packit/v2"

// Detect returns a DetectFunc that:
//   - Provides "pnpm" so downstream buildpacks can request it.
//   - Requires "node" because pnpm is a Node.js tool and cannot run without a
//     Node.js runtime being present on PATH. Declaring this requirement allows
//     the CNB platform to order a Node.js provider buildpack ahead of this one
//     and fail the build fast if no provider is available.
func Detect() packit.DetectFunc {
	return func(context packit.DetectContext) (packit.DetectResult, error) {
		return packit.DetectResult{
			Plan: packit.BuildPlan{
				Provides: []packit.BuildPlanProvision{
					{Name: PNPMDependency},
				},
				Requires: []packit.BuildPlanRequirement{
					{Name: "node"},
				},
			},
		}, nil
	}
}
