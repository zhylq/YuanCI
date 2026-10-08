package pipeline

import (
	"strings"
	"testing"
	"time"
)

func TestExtractTriggersIgnoresExecutableDecoderErrors(t *testing.T) {
	for name, source := range map[string]string{
		"unknown job field":  strings.Replace(validPipeline, "        image: golang:1.27", "        image: golang:1.27\n        unknown_job_field: true", 1),
		"unknown root field": validPipeline + "unknown: true\n",
		"wrong stages type":  "version: v1\nname: webhook\nstages: invalid\ntriggers: [{event: push, branches: [main]}]\n",
	} {
		t.Run(name, func(t *testing.T) {
			triggers, err := ExtractTriggers([]byte(source))
			if err != nil || len(triggers) != 1 || triggers[0].Event != "push" {
				t.Fatalf("unrelated executable decoder error erased policy: triggers=%+v err=%v", triggers, err)
			}
			if _, err := Compile([]byte(source), time.Now()); err == nil {
				t.Fatal("independent policy extraction weakened executable validation")
			}
		})
	}
}

func TestExtractTriggersRejectsAmbiguousOrInvalidPolicy(t *testing.T) {
	prefix := strings.Replace(validPipeline, "triggers:\n  - event: push\n", "", 1)
	for name, suffix := range map[string]string{
		"duplicate root triggers":      "triggers: [{event: push}]\ntriggers: [{event: tag}]\n",
		"duplicate unrelated root key": "name: duplicate\ntriggers: [{event: push}]\n",
		"non-string root key":          "1: value\ntriggers: [{event: push}]\n",
		"root merge":                   "<<: {triggers: [{event: push}]}\n",
		"duplicate trigger key":        "triggers: [{event: push, event: tag}]\n",
		"non-string trigger key":       "triggers: [{event: push, 1: value}]\n",
		"trigger merge":                "triggers: [{<<: {event: push}}]\n",
		"trigger alias":                "triggers:\n  - &trigger {event: push}\n  - *trigger\n",
		"branch alias":                 "triggers:\n  - event: push\n    branches: [&branch main, *branch]\n",
		"multiple documents":           "triggers: [{event: push}]\n---\ntriggers: [{event: tag}]\n",
		"malformed trailing document":  "triggers: [{event: push}]\n---\ninvalid: [\n",
		"unknown trigger field":        "triggers: [{event: push, branch: main}]\n",
		"wrong trigger type":           "triggers: {event: push}\n",
		"wrong branches type":          "triggers: [{event: push, branches: {main: true}}]\n",
		"unsupported event":            "triggers: [{event: deployment}]\n",
		"unsupported paths":            "triggers: [{event: push, paths: ['src/**']}]\n",
		"unsupported glob":             "triggers: [{event: push, branches: ['release/*']}]\n",
	} {
		t.Run(name, func(t *testing.T) {
			source := []byte(prefix + suffix)
			if _, err := ExtractTriggers(source); err == nil {
				t.Fatal("invalid or ambiguous trigger policy accepted")
			}
			if _, err := Compile(source, time.Now()); err == nil {
				t.Fatal("rejected policy compiled to executable YAML with hidden triggers")
			}
		})
	}
}

func TestExtractTriggersPreservesLegacyEmptyPoliciesAndExecutableAliases(t *testing.T) {
	prefix := strings.Replace(validPipeline, "triggers:\n  - event: push\n", "", 1)
	for _, suffix := range []string{"", "triggers: []\n", "triggers: null\n"} {
		triggers, err := ExtractTriggers([]byte(prefix + suffix))
		if err != nil || len(triggers) != 0 {
			t.Fatalf("legacy policy changed: triggers=%+v err=%v", triggers, err)
		}
	}
	// YAML reuse in executable configuration is unrelated to event policy.
	source := strings.Replace(validPipeline, "        image: golang:1.27", "        <<: {image: golang:1.27}", 1)
	source = strings.Replace(source, `commands: ["go test ./..."]`, `commands: &commands ["go test ./..."]`, 1)
	source = strings.Replace(source, `commands: ["docker build ."]`, `commands: *commands`, 1)
	triggers, err := ExtractTriggers([]byte(source))
	if err != nil || len(triggers) != 1 {
		t.Fatalf("executable YAML reuse erased trigger policy: %v", err)
	}
	if _, err := Compile([]byte(source), time.Now()); err != nil {
		t.Fatalf("executable YAML reuse rejected: %v", err)
	}
}
