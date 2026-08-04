package pnpm_test

import (
	"testing"

	"github.com/sclevine/spec"
	"github.com/sclevine/spec/report"
)

func TestUnitPNPM(t *testing.T) {
	suite := spec.New("pnpm", spec.Report(report.Terminal{}), spec.Parallel())
	suite("Build", testBuild, spec.Sequential())
	suite("Detect", testDetect)
	suite.Run(t)
}
