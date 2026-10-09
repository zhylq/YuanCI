package pipeline

import (
	"fmt"
	"strings"

	"github.com/yuanci/yuanci/internal/scm"
)

// ValidateTriggers validates event policy independently of executable stages so
// unrelated configuration errors cannot turn excluded events into failed runs.
func ValidateTriggers(triggers []Trigger) error {
	if problems := validateTriggers(triggers); len(problems) > 0 {
		return problems
	}
	return nil
}

func validateTriggers(triggers []Trigger) ValidationErrors {
	var problems ValidationErrors
	allowedEvents := map[string]bool{"push": true, "pull_request": true, "tag": true, "manual": true, "cron": true, "api": true}
	for i, trigger := range triggers {
		path := fmt.Sprintf("triggers[%d]", i)
		if !allowedEvents[trigger.Event] {
			problems = append(problems, ValidationError{path + ".event", "is not supported"})
		}
		if len(trigger.Paths) > 0 {
			problems = append(problems, ValidationError{path + ".paths", "path filters are not supported"})
		}
		if len(trigger.Branches) > 0 && trigger.Event != "push" && trigger.Event != "pull_request" {
			problems = append(problems, ValidationError{path + ".branches", "branch filters are supported only for push and pull_request"})
		}
		for j, branch := range trigger.Branches {
			if !validBranchName(branch) {
				problems = append(problems, ValidationError{fmt.Sprintf("%s.branches[%d]", path, j), "must be an exact unqualified branch name; glob patterns are not supported"})
			}
		}
	}
	return problems
}

// MatchTriggers matches the actual SCM event, treating triggers as alternatives.
// An empty list supplies no YAML restriction; orchestration applies the legacy
// project event settings in that case. Branches are exact, case-sensitive names.
func MatchTriggers(triggers []Trigger, event scm.Event) bool {
	if len(triggers) == 0 {
		return true
	}
	var branch string
	switch event.Type {
	case scm.EventPush:
		if !strings.HasPrefix(event.Ref, "refs/heads/") {
			return false
		}
		branch = strings.TrimPrefix(event.Ref, "refs/heads/")
		if !validBranchName(branch) {
			return false
		}
	case scm.EventPullRequest:
		if !strings.HasPrefix(event.Ref, "refs/heads/") || !validBranchName(strings.TrimPrefix(event.Ref, "refs/heads/")) {
			return false
		}
		branch = event.Metadata["base_ref"]
		if !validBranchName(branch) {
			return false
		}
	case scm.EventTag:
		if !strings.HasPrefix(event.Ref, "refs/tags/") || !validGitRef(event.Ref) {
			return false
		}
	default:
		return false
	}
	for _, trigger := range triggers {
		if trigger.Event != string(event.Type) || len(trigger.Paths) != 0 {
			continue
		}
		if len(trigger.Branches) == 0 {
			return true
		}
		for _, wanted := range trigger.Branches {
			if branch == wanted {
				return true
			}
		}
	}
	return false
}

func validBranchName(branch string) bool {
	return branch != "" && len(branch) <= 255 && branch != "@" && !strings.HasPrefix(branch, "-") && !strings.HasPrefix(branch, "refs/") && validGitRef("refs/heads/"+branch)
}

// Full tag refs permit names such as @, -release and refs/release. Only branch
// filters require unqualified branch names and their additional restrictions.
func validGitRef(ref string) bool {
	if !strings.Contains(ref, "/") || strings.HasSuffix(ref, ".") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.ContainsAny(ref, "~^:?*[\\") {
		return false
	}
	for _, r := range ref {
		if r <= ' ' || r == 127 {
			return false
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}
