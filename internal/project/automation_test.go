package project

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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

func TestAutomationValidationRequiresBoundedImmutableIdentity(t *testing.T) {
	valid := AutomationValidation{RepositoryID: uuid.New(), AppRevision: uuid.New(), PipelinePath: DefaultPipelinePath,
		CommitSHA: strings.Repeat("a", 40), ConfigSHA256: strings.Repeat("b", 64), PipelineName: "test",
		ValidatedAt: time.Now(), SettingsRevision: 0}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*AutomationValidation){
		func(v *AutomationValidation) { v.RepositoryID = uuid.Nil },
		func(v *AutomationValidation) { v.AppRevision = uuid.Nil },
		func(v *AutomationValidation) { v.SettingsRevision = -1 },
		func(v *AutomationValidation) { v.CommitSHA = strings.Repeat("A", 40) },
		func(v *AutomationValidation) { v.ConfigSHA256 = strings.Repeat("b", 63) },
		func(v *AutomationValidation) { v.PipelineName = "" },
		func(v *AutomationValidation) { v.ValidatedAt = time.Time{} },
	} {
		candidate := valid
		change(&candidate)
		if err := candidate.Validate(); !errors.Is(err, ErrAutomationInvalid) {
			t.Fatalf("invalid proof accepted: %#v", candidate)
		}
	}
}
