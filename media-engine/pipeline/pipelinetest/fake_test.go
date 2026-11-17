package pipelinetest

import (
	"testing"

	"nexostream/media-engine/pipeline"
)

// The fake engine itself must honor the contract it stands in for, or tests
// built on it would prove nothing.
func TestFakeEngineConforms(t *testing.T) {
	RunConformance(t, Conformance{
		New:   func(*testing.T) pipeline.Engine { return &Engine{} },
		Spec:  func(_ *testing.T, _ string) pipeline.Spec { return pipeline.Spec{ChannelID: "fake-1"} },
		Input: func(*testing.T) []byte { return make([]byte, 188*14) },
	})
}
