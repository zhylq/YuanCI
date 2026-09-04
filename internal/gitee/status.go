package gitee

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/commitstatus"
	"github.com/yuanci/yuanci/internal/identity"
)

type CheckProvider interface {
	RepositoryProvider
	DeliverCheck(context.Context, string, Repository, commitstatus.Item, string) error
}

// Deliver uses the shared durable outbox; broad OAuth credentials never leave
// the control plane. A per-Run name separates reruns of the same commit.
func (s *Service) Deliver(ctx context.Context, item commitstatus.Item) error {
	if item.Provider != "gitee" || item.RepositoryID == uuid.Nil || item.RunID == uuid.Nil || !shaPattern.MatchString(item.CommitSHA) || !item.State.Valid() {
		return commitstatus.ErrInvalid
	}
	store, ok := s.Store.(AutomationStore)
	if !ok {
		return ErrStale
	}
	provider, ok := s.Provider.(CheckProvider)
	if !ok {
		return ErrRemote
	}
	binding, err := store.ResolveGiteeRepository(ctx, item.RepositoryExternalID)
	if err != nil {
		return err
	}
	if binding.ProjectID != item.RepositoryID {
		return commitstatus.ErrInvalid
	}
	token, err := s.Access(ctx, binding.GrantID)
	if err != nil {
		return err
	}
	defer clear(token)
	remote, err := provider.Repository(ctx, string(token), binding.Owner, binding.Name)
	if err != nil {
		return err
	}
	if remote.ID != binding.ID || remote.AccountID != binding.AccountID {
		return ErrStale
	}
	return provider.DeliverCheck(ctx, string(token), binding.Repository, item, s.Origin+"/runs/"+item.RunID.String())
}

type checkRun struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	SHA    string `json:"head_sha"`
	Status string `json:"status"`
}

func (c *Client) DeliverCheck(ctx context.Context, token string, repo Repository, item commitstatus.Item, target string) error {
	if !validToken(token) || !ValidComponent(repo.Owner) || !ValidComponent(repo.Name) || item.RunID == uuid.Nil || !shaPattern.MatchString(item.CommitSHA) || !item.State.Valid() {
		return commitstatus.ErrInvalid
	}
	name := "YuanCI/" + item.RunID.String()
	base := "/repos/" + repo.Owner + "/" + repo.Name
	var found *checkRun
	// Bounded reconciliation also recovers an accepted POST whose response was lost.
	// Gitee has no documented idempotency key: concurrent remote creates cannot be
	// claimed exactly-once. The durable worker lease serializes normal delivery.
	for page := 1; page <= 10; page++ {
		var checks []checkRun
		query := url.Values{"check_name": {name}, "filter": {"all"}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if err := c.get(ctx, base+"/commits/"+item.CommitSHA+"/check-runs?"+query.Encode(), token, &checks, 1<<20); err != nil {
			return err
		}
		for _, check := range checks {
			if check.ID > 0 && check.Name == name && check.SHA == item.CommitSHA {
				if found == nil || check.ID > found.ID {
					copy := check
					found = &copy
				}
			}
		}
		if len(checks) < 100 {
			break
		}
		if page == 10 {
			return ErrRemote
		}
	}
	if found != nil && found.Status == "completed" && item.State == commitstatus.StatePending {
		return nil
	}
	status, conclusion := "completed", "failure"
	switch item.State {
	case commitstatus.StatePending:
		status, conclusion = "queued", ""
	case commitstatus.StateSuccess:
		conclusion = "success"
	case commitstatus.StateError:
		conclusion = "cancelled"
	}
	body := map[string]any{"name": name, "status": status, "details_url": target, "output": map[string]any{"title": "YuanCI", "summary": item.Description, "annotations": []any{}, "images": []any{}}, "actions": []any{}}
	if conclusion != "" {
		body["conclusion"] = conclusion
		body["completed_at"] = time.Now().UTC().Format(time.RFC3339)
	}
	method, path := "POST", base+"/check-runs"
	if found != nil {
		method = "PATCH"
		path += "/" + strconv.FormatInt(found.ID, 10)
	} else {
		body["head_sha"] = item.CommitSHA
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return ErrRemote
	}
	request, err := http.NewRequestWithContext(ctx, method, identity.GiteeInstance+"/api/v5"+path, bytes.NewReader(encoded))
	if err != nil {
		return ErrRemote
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	return c.request(request, nil, 1<<20)
}
