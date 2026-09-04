package main

import (
	"context"
	"github.com/yuanci/yuanci/internal/commitstatus"

	"github.com/yuanci/yuanci/internal/gitee"
	"github.com/yuanci/yuanci/internal/githubapp"
	"github.com/yuanci/yuanci/internal/scm"
)

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
