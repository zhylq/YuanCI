package project

import (
	"errors"
	"strings"
	"testing"
)

func TestAutomationUpdateValidation(t *testing.T) {
	valid := AutomationUpdate{PipelinePath: DefaultPipelinePath, TriggerPush: true}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*AutomationUpdate){
		func(v *AutomationUpdate) { v.ExpectedRevision = -1 },
		func(v *AutomationUpdate) { v.PipelinePath = "" },
		func(v *AutomationUpdate) { v.PipelinePath = "/.yuanci.yml" },
		func(v *AutomationUpdate) { v.PipelinePath = "../.yuanci.yml" },
		func(v *AutomationUpdate) { v.PipelinePath = "ci/../pipeline.yml" },
		func(v *AutomationUpdate) { v.PipelinePath = "ci/../.yuanci.yml" },
		func(v *AutomationUpdate) { v.PipelinePath = `ci\\pipeline.yml` },
		func(v *AutomationUpdate) { v.PipelinePath = "ci/pipeline.json" },
		func(v *AutomationUpdate) { v.PipelinePath = "ci/\x00pipeline.yml" },
		func(v *AutomationUpdate) { v.PipelinePath = strings.Repeat("a", 257) + ".yml" },
		func(v *AutomationUpdate) { v.Enabled, v.TriggerPush = true, false },
	} {
		candidate := valid
		change(&candidate)
		if err := candidate.Validate(); !errors.Is(err, ErrAutomationInvalid) {
			t.Fatalf("invalid update accepted: %#v, error=%v", candidate, err)
		}
	}
}
