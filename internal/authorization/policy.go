// Package authorization evaluates permissions over trusted, server-resolved
// identities, grants and resource ancestry. It performs no authentication or I/O.
package authorization

import (
	"slices"

	"github.com/google/uuid"
)

type ScopeKind string

const (
	Instance     ScopeKind = "instance"
	Organization ScopeKind = "organization"
	Project      ScopeKind = "project"
	Environment  ScopeKind = "environment"
)

type Scope struct {
	Kind ScopeKind
	ID   uuid.UUID
}

type Protection string

const (
	Protected   Protection = "protected"
	Unprotected Protection = "unprotected"
)

// Resource.Path must include the instance and every ancestor through the target.
// Environment resources must have an explicit protection classification.
type Resource struct {
	Path       []Scope
	Protection Protection
}

type Principal struct {
	ID     uuid.UUID
	Active bool
}

type Role string

const (
	Viewer     Role = "viewer"
	Developer  Role = "developer"
	Maintainer Role = "maintainer"
	Admin      Role = "admin"
	Deployer   Role = "deployer"
	Approver   Role = "approver"
)

type Grant struct {
	SubjectID uuid.UUID
	Scope     Scope
	Role      Role
}

type Action string

const (
	ResourceRead      Action = "resource.read"
	RunRead           Action = "run.read"
	RunCreate         Action = "run.create"
	RunCancel         Action = "run.cancel"
	PipelineWrite     Action = "pipeline.write"
	RepositoryManage  Action = "repository.manage"
	SecretManage      Action = "secret.manage"
	EnvironmentManage Action = "environment.manage"
	DeploymentRead    Action = "deployment.read"
	DeploymentCreate  Action = "deployment.create"
	DeploymentApprove Action = "deployment.approve"
	MembersManage     Action = "members.manage"
	AuditRead         Action = "audit.read"
	RunnerManage      Action = "runner.manage"
	InstanceManage    Action = "instance.manage"
)

type Request struct {
	Action   Action
	Resource Resource
	// RequestedBy is required for approval and must come from the persisted
	// deployment, not the client's submitted identity.
	RequestedBy uuid.UUID
}

// Allowed is default-deny, including for the zero value of any input.
// Grants and ancestry must never be accepted from an untrusted request body.
func Allowed(principal Principal, request Request, grants []Grant) bool {
	if !principal.Active || principal.ID == uuid.Nil || !validResource(request.Resource) {
		return false
	}
	path := request.Resource.Path
	target := path[len(path)-1]
	if !validActionTarget(request.Action, target.Kind) {
		return false
	}
	if request.Action == DeploymentApprove && (request.RequestedBy == uuid.Nil || request.RequestedBy == principal.ID) {
		return false
	}
	exact := request.Action == DeploymentApprove ||
		(request.Resource.Protection == Protected && protectedMutation(request.Action))
	for _, grant := range grants {
		if grant.SubjectID != principal.ID || !roleAllows(grant.Role, request.Action) {
			continue
		}
		// Deployment-specific roles cannot be granted at broader scopes.
		if (grant.Role == Deployer || grant.Role == Approver) && grant.Scope.Kind != Environment {
			continue
		}
		if exact && grant.Scope != target {
			continue
		}
		if slices.Contains(path, grant.Scope) {
			return true
		}
	}
	return false
}

func validResource(resource Resource) bool {
	order := [...]ScopeKind{Instance, Organization, Project, Environment}
	if len(resource.Path) < 1 || len(resource.Path) > len(order) {
		return false
	}
	for i, scope := range resource.Path {
		if scope.Kind != order[i] || scope.ID == uuid.Nil {
			return false
		}
		for _, previous := range resource.Path[:i] {
			if previous.ID == scope.ID {
				return false
			}
		}
	}
	if len(resource.Path) == 4 {
		return resource.Protection == Protected || resource.Protection == Unprotected
	}
	return resource.Protection == ""
}

func validActionTarget(action Action, kind ScopeKind) bool {
	switch action {
	case ResourceRead, MembersManage, AuditRead:
		return true // validResource has already validated the scope kind.
	case RunRead, RunCreate, RunCancel, PipelineWrite, RepositoryManage:
		return kind == Project
	case SecretManage:
		return kind == Organization || kind == Project || kind == Environment
	case EnvironmentManage, DeploymentRead, DeploymentCreate, DeploymentApprove:
		return kind == Environment
	case RunnerManage, InstanceManage:
		return kind == Instance
	default:
		return false
	}
}

func protectedMutation(action Action) bool {
	switch action {
	case SecretManage, EnvironmentManage, DeploymentCreate, DeploymentApprove, MembersManage:
		return true
	default:
		return false
	}
}

func roleAllows(role Role, action Action) bool {
	switch role {
	case Viewer:
		return slices.Contains([]Action{ResourceRead, RunRead, DeploymentRead}, action)
	case Developer:
		return slices.Contains([]Action{ResourceRead, RunRead, RunCreate, RunCancel, PipelineWrite, DeploymentRead}, action)
	case Maintainer:
		return slices.Contains([]Action{ResourceRead, RunRead, RunCreate, RunCancel, PipelineWrite,
			RepositoryManage, SecretManage, EnvironmentManage, DeploymentRead, DeploymentCreate}, action)
	case Admin:
		// An administrator is not implicitly an approver; no wildcard permission.
		return slices.Contains([]Action{ResourceRead, RunRead, RunCreate, RunCancel, PipelineWrite,
			RepositoryManage, SecretManage, EnvironmentManage, DeploymentRead, DeploymentCreate,
			MembersManage, AuditRead, RunnerManage, InstanceManage}, action)
	case Deployer:
		return slices.Contains([]Action{ResourceRead, DeploymentRead, DeploymentCreate}, action)
	case Approver:
		return slices.Contains([]Action{ResourceRead, DeploymentRead, DeploymentApprove}, action)
	default:
		return false
	}
}
