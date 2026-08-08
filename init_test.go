package pnpm_test

import (
	"testing"

	"github.com/sclevine/spec"
	"github.com/sclevine/spec/report"
)

func TestUnitPNPM(t *testing.T) {
	suite := spec.New("pnpm", spec.Report(report.Terminal{}), spec.Sequential())
	suite("Build", testBuild, spec.Sequential())
	suite("Detect", testDetect, spec.Sequential())
	suite.Run(t)
}