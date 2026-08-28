package authorization

import (
	"testing"

	"github.com/google/uuid"
)

var (
	actor    = Principal{ID: uuid.New(), Active: true}
	ancestry = []Scope{{Kind: Instance, ID: uuid.New()}, {Kind: Organization, ID: uuid.New()},
		{Kind: Project, ID: uuid.New()}, {Kind: Environment, ID: uuid.New()}}
)

func resource(depth int) Resource {
	r := Resource{Path: append([]Scope(nil), ancestry[:depth]...)}
	if depth == 4 {
		r.Protection = Unprotected
	}
	return r
}

// This explicit expectation table is independent of the implementation.
func TestRoleActionScopeMatrix(t *testing.T) {
	roles := map[Role][]Action{
		Viewer:    {ResourceRead, RunRead, DeploymentRead},
		Developer: {ResourceRead, RunRead, RunCreate, RunCancel, PipelineWrite, DeploymentRead},
		Maintainer: {ResourceRead, RunRead, RunCreate, RunCancel, PipelineWrite, RepositoryManage,
			SecretManage, EnvironmentManage, DeploymentRead, DeploymentCreate},
		Admin: {ResourceRead, RunRead, RunCreate, RunCancel, PipelineWrite, RepositoryManage,
			SecretManage, EnvironmentManage, DeploymentRead, DeploymentCreate, MembersManage,
			AuditRead, RunnerManage, InstanceManage},
		Deployer: {ResourceRead, DeploymentRead, DeploymentCreate},
		Approver: {ResourceRead, DeploymentRead, DeploymentApprove},
	}
	allowedTargets := map[Action][]int{
		ResourceRead: {1, 2, 3, 4}, RunRead: {3}, RunCreate: {3}, RunCancel: {3},
		PipelineWrite: {3}, RepositoryManage: {3}, SecretManage: {2, 3, 4},
		EnvironmentManage: {4}, DeploymentRead: {4}, DeploymentCreate: {4},
		DeploymentApprove: {4}, MembersManage: {1, 2, 3, 4}, AuditRead: {1, 2, 3, 4},
		RunnerManage: {1}, InstanceManage: {1},
	}
	for role, permitted := range roles {
		for action, targets := range allowedTargets {
			for depth := 1; depth <= 4; depth++ {
				for grantDepth := 1; grantDepth <= 4; grantDepth++ {
					roleAllows := false
					for _, a := range permitted {
						roleAllows = roleAllows || a == action
					}
					targetAllows := false
					for _, d := range targets {
						targetAllows = targetAllows || d == depth
					}
					want := roleAllows && targetAllows && grantDepth <= depth
					if role == Deployer || role == Approver {
						want = want && grantDepth == 4
					}
					request := Request{Action: action, Resource: resource(depth), RequestedBy: uuid.New()}
					grants := []Grant{{SubjectID: actor.ID, Role: role, Scope: ancestry[grantDepth-1]}}
					if got := Allowed(actor, request, grants); got != want {
						t.Errorf("role=%s action=%s target=%d grant=%d: got %v want %v", role, action, depth, grantDepth, got, want)
					}
				}
			}
		}
	}
}

func TestProtectedEnvironmentRequiresExactGrant(t *testing.T) {
	r := resource(4)
	r.Protection = Protected
	for _, action := range []Action{SecretManage, EnvironmentManage, DeploymentCreate, DeploymentApprove, MembersManage} {
		for _, role := range []Role{Admin, Maintainer, Deployer, Approver} {
			for depth := 1; depth <= 3; depth++ {
				if Allowed(actor, Request{Action: action, Resource: r, RequestedBy: uuid.New()},
					[]Grant{{SubjectID: actor.ID, Scope: ancestry[depth-1], Role: role}}) {
					t.Errorf("inherited %s grant allowed protected %s at depth %d", role, action, depth)
				}
			}
		}
	}
	for action, role := range map[Action]Role{SecretManage: Maintainer, EnvironmentManage: Maintainer,
		DeploymentCreate: Deployer, DeploymentApprove: Approver, MembersManage: Admin} {
		if !Allowed(actor, Request{Action: action, Resource: r, RequestedBy: uuid.New()},
			[]Grant{{SubjectID: actor.ID, Scope: ancestry[3], Role: role}}) {
			t.Errorf("explicit environment grant denied for %s", action)
		}
	}
	if !Allowed(actor, Request{Action: DeploymentRead, Resource: r},
		[]Grant{{SubjectID: actor.ID, Scope: ancestry[2], Role: Viewer}}) {
		t.Fatal("protected environment should remain visible to scoped viewer")
	}
}

func TestDenyInvalidOrOutOfScopeInputs(t *testing.T) {
	valid := Request{Action: RunCreate, Resource: resource(3)}
	grant := Grant{SubjectID: actor.ID, Scope: ancestry[2], Role: Developer}
	cases := []struct {
		name      string
		principal Principal
		request   Request
		grants    []Grant
	}{
		{"anonymous", Principal{}, valid, []Grant{grant}},
		{"suspended", Principal{ID: actor.ID}, valid, []Grant{grant}},
		{"no grants", actor, valid, nil},
		{"unknown action", actor, Request{Action: "*", Resource: resource(3)}, []Grant{grant}},
		{"unknown role", actor, valid, []Grant{{SubjectID: actor.ID, Scope: ancestry[2], Role: "owner"}}},
		{"another subject", actor, valid, []Grant{{SubjectID: uuid.New(), Scope: ancestry[2], Role: Admin}}},
		{"another project", actor, valid, []Grant{{SubjectID: actor.ID, Scope: Scope{Kind: Project, ID: uuid.New()}, Role: Admin}}},
		{"another organization", actor, valid, []Grant{{SubjectID: actor.ID, Scope: Scope{Kind: Organization, ID: uuid.New()}, Role: Admin}}},
		{"another instance", actor, valid, []Grant{{SubjectID: actor.ID, Scope: Scope{Kind: Instance, ID: uuid.New()}, Role: Admin}}},
		{"scope type confusion", actor, valid, []Grant{{SubjectID: actor.ID, Scope: Scope{Kind: Organization, ID: ancestry[2].ID}, Role: Admin}}},
		{"empty path", actor, Request{Action: RunCreate}, []Grant{grant}},
		{"skipped ancestor", actor, Request{Action: RunCreate, Resource: Resource{Path: []Scope{ancestry[0], ancestry[2]}}}, []Grant{grant}},
		{"wrong target type", actor, Request{Action: RunCreate, Resource: resource(2)}, []Grant{{SubjectID: actor.ID, Scope: ancestry[1], Role: Admin}}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if Allowed(tt.principal, tt.request, tt.grants) {
				t.Fatal("access should be denied")
			}
		})
	}
	for _, protection := range []Protection{"", "unexpected"} {
		r := resource(4)
		r.Protection = protection
		if Allowed(actor, Request{Action: ResourceRead, Resource: r}, []Grant{{SubjectID: actor.ID, Scope: ancestry[0], Role: Admin}}) {
			t.Fatal("unclassified environment allowed")
		}
	}
	for index := range ancestry {
		r := resource(4)
		r.Path[index].ID = uuid.Nil
		if Allowed(actor, Request{Action: ResourceRead, Resource: r}, []Grant{{SubjectID: actor.ID, Scope: ancestry[0], Role: Admin}}) {
			t.Fatal("nil ancestor allowed")
		}
	}
	r := resource(4)
	r.Path[3].ID = r.Path[2].ID
	if Allowed(actor, Request{Action: ResourceRead, Resource: r}, []Grant{{SubjectID: actor.ID, Scope: ancestry[0], Role: Admin}}) {
		t.Fatal("duplicate ancestor ID allowed")
	}
}

func TestApprovalSeparationAndRoleUnion(t *testing.T) {
	request := Request{Action: DeploymentApprove, Resource: resource(4)}
	grants := []Grant{{SubjectID: actor.ID, Scope: ancestry[3], Role: Approver},
		{SubjectID: actor.ID, Scope: ancestry[0], Role: Admin}}
	for _, requester := range []uuid.UUID{uuid.Nil, actor.ID} {
		request.RequestedBy = requester
		if Allowed(actor, request, grants) {
			t.Fatal("missing requester or self-approval allowed")
		}
	}
	request.RequestedBy = uuid.New()
	if !Allowed(actor, request, grants) {
		t.Fatal("independent approver denied")
	}
	if Allowed(actor, request, grants[1:]) {
		t.Fatal("administrator implicitly became approver")
	}
	// An unrelated grant must not prevent a valid explicit grant from working.
	grants = append([]Grant{{SubjectID: uuid.New(), Scope: ancestry[3], Role: Admin}}, grants...)
	if !Allowed(actor, request, grants) {
		t.Fatal("unrelated grant overrides valid grant")
	}
	request.Resource.Path[3].ID = uuid.New()
	if Allowed(actor, request, grants) {
		t.Fatal("approval crossed environment boundary")
	}
}
