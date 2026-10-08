package pipeline

import (
	"testing"

	"github.com/yuanci/yuanci/internal/scm"
)

func TestMatchTriggersMatchesActualEventAndExactBranch(t *testing.T) {
	for _, test := range []struct {
		name     string
		triggers []Trigger
		event    scm.Event
		want     bool
	}{
		{"legacy unrestricted", nil, scm.Event{}, true},
		{"main push", []Trigger{{Event: "push", Branches: []string{"main"}}}, scm.Event{Type: scm.EventPush, Ref: "refs/heads/main"}, true},
		{"nested branch", []Trigger{{Event: "push", Branches: []string{"release/stable"}}}, scm.Event{Type: scm.EventPush, Ref: "refs/heads/release/stable"}, true},
		{"exact branch", []Trigger{{Event: "push", Branches: []string{"main"}}}, scm.Event{Type: scm.EventPush, Ref: "refs/heads/main-next"}, false},
		{"case sensitive", []Trigger{{Event: "push", Branches: []string{"main"}}}, scm.Event{Type: scm.EventPush, Ref: "refs/heads/Main"}, false},
		{"PR target", []Trigger{{Event: "pull_request", Branches: []string{"main"}}}, scm.Event{Type: scm.EventPullRequest, Ref: "refs/heads/feature", Metadata: map[string]string{"base_ref": "main"}}, true},
		{"PR head irrelevant", []Trigger{{Event: "pull_request", Branches: []string{"main"}}}, scm.Event{Type: scm.EventPullRequest, Ref: "refs/heads/main", Metadata: map[string]string{"base_ref": "release"}}, false},
		{"missing PR base", []Trigger{{Event: "pull_request"}}, scm.Event{Type: scm.EventPullRequest, Ref: "refs/heads/feature"}, false},
		{"tag event", []Trigger{{Event: "tag"}}, scm.Event{Type: scm.EventTag, Ref: "refs/tags/v1.0"}, true},
		{"tag named refs release", []Trigger{{Event: "tag"}}, scm.Event{Type: scm.EventTag, Ref: "refs/tags/refs/release"}, true},
		{"tag named at sign", []Trigger{{Event: "tag"}}, scm.Event{Type: scm.EventTag, Ref: "refs/tags/@"}, true},
		{"tag starts with hyphen", []Trigger{{Event: "tag"}}, scm.Event{Type: scm.EventTag, Ref: "refs/tags/-release"}, true},
		{"tag does not match push", []Trigger{{Event: "push"}}, scm.Event{Type: scm.EventTag, Ref: "refs/tags/main"}, false},
		{"push must not contain tag ref", []Trigger{{Event: "push"}}, scm.Event{Type: scm.EventPush, Ref: "refs/tags/main"}, false},
		{"tag must not contain branch ref", []Trigger{{Event: "tag"}}, scm.Event{Type: scm.EventTag, Ref: "refs/heads/main"}, false},
		{"unsupported event", []Trigger{{Event: "manual"}}, scm.Event{Type: "manual"}, false},
		{"OR triggers", []Trigger{{Event: "tag"}, {Event: "push", Branches: []string{"other", "main"}}}, scm.Event{Type: scm.EventPush, Ref: "refs/heads/main"}, true},
		{"unsupported persisted paths", []Trigger{{Event: "push", Paths: []string{"src/**"}}}, scm.Event{Type: scm.EventPush, Ref: "refs/heads/main"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := MatchTriggers(test.triggers, test.event); got != test.want {
				t.Fatalf("MatchTriggers()=%v want %v", got, test.want)
			}
		})
	}
}

func TestMatchTriggersRejectsMalformedTagRefs(t *testing.T) {
	for _, ref := range []string{"", "v1", "refs/tags/", "refs/tags//release", "refs/tags/release/", "refs/tags/release.", "refs/tags/.hidden", "refs/tags/release.lock", "refs/tags/release..next", "refs/tags/release@{next}", "refs/tags/release*", "refs/tags/release\n", "refs/tags/release\\next"} {
		t.Run(ref, func(t *testing.T) {
			if MatchTriggers([]Trigger{{Event: "tag"}}, scm.Event{Type: scm.EventTag, Ref: ref}) {
				t.Fatalf("invalid tag matched: %q", ref)
			}
		})
	}
}

func TestMatchTriggersRejectsMalformedBranchRefsEvenWithoutBranchFilter(t *testing.T) {
	for _, ref := range []string{"", "main", "refs/heads/", "refs/heads//main", "refs/heads/main/", "refs/heads/main.", "refs/heads/.hidden", "refs/heads/a/.hidden", "refs/heads/main.lock", "refs/heads/a.lock/main", "refs/heads/main..next", "refs/heads/main@{next}", "refs/heads/main*", "refs/heads/main\n", "refs/heads/main\\next", "refs/heads/-main", "refs/heads/refs/heads/main"} {
		t.Run(ref, func(t *testing.T) {
			if MatchTriggers([]Trigger{{Event: "push"}}, scm.Event{Type: scm.EventPush, Ref: ref}) {
				t.Fatalf("invalid ref matched: %q", ref)
			}
		})
	}
}
