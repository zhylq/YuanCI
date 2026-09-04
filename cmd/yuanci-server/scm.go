package main

import (
	"context"
	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/commitstatus"
	runmodel "github.com/yuanci/yuanci/internal/run"

	"github.com/yuanci/yuanci/internal/gitee"
	"github.com/yuanci/yuanci/internal/githubapp"
	"github.com/yuanci/yuanci/internal/scm"
)

type credentialRouter struct {
	github *githubapp.Service
	gitee  *gitee.CheckoutBroker
}

func (c credentialRouter) IssueCheckoutCredential(ctx context.Context, id uuid.UUID, external string) (githubapp.CheckoutCredential, error) {
	if c.github == nil {
		return githubapp.CheckoutCredential{}, githubapp.ErrCredentialUnavailable
	}
	return c.github.IssueCheckoutCredential(ctx, id, external)
}
func (c credentialRouter) IssueAssignmentCredential(ctx context.Context, runner uuid.UUID, a *runmodel.Assignment) (githubapp.CheckoutCredential, error) {
	if c.gitee == nil {
		return githubapp.CheckoutCredential{}, githubapp.ErrCredentialUnavailable
	}
	return c.gitee.IssueAssignmentCredential(ctx, runner, a)
}

type statusRouter struct {
	github commitstatus.DeliveryProvider
	gitee  *gitee.Service
}

func (s statusRouter) Deliver(ctx context.Context, item commitstatus.Item) error {
	if item.Provider == "gitee" && s.gitee != nil {
		return s.gitee.Deliver(ctx, item)
	}
	if item.Provider == "github" && s.github != nil {
		return s.github.Deliver(ctx, item)
	}
	return commitstatus.ErrInvalid
}

type pipelineRouter struct {
	github *githubapp.Service
	gitee  *gitee.Service
}

func (p pipelineRouter) FetchPipeline(ctx context.Context, event scm.Event, path string) (githubapp.Repository, []byte, error) {
	switch event.Provider {
	case scm.GitHub:
		if p.github != nil {
			return p.github.FetchPipeline(ctx, event, path)
		}
	case scm.Gitee:
		if p.gitee != nil {
			return p.gitee.FetchPipeline(ctx, event, path)
		}
	}
	return githubapp.Repository{}, nil, githubapp.ErrInvalidEvent
}
