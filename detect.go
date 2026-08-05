package pnpm

import "github.com/paketo-buildpacks/packit/v2"

// Detect returns a DetectFunc that:
//   - Provides "pnpm" so downstream buildpacks can request it.
//
// Note: the buildpack's public API intentionally does NOT declare a
// build-plan "requires" on "node", even though pnpm is unusable without a
// Node.js runtime at run time. This mirrors the upstream Yarn CNB's
// behavior (see README): the buildpack always participates and installs
// pnpm onto $PATH, leaving it to the app/operator to ensure a Node.js
// provider buildpack is present earlier in the group.
func Detect() packit.DetectFunc {
	return func(context packit.DetectContext) (packit.DetectResult, error) {
		return packit.DetectResult{
			Plan: packit.BuildPlan{
				Provides: []packit.BuildPlanProvision{
					{Name: PNPMDependency},
				},
			},
		}, nil
	}
}
