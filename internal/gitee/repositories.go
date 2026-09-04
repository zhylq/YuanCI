package gitee

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/integration"
	"github.com/yuanci/yuanci/internal/scm"
)

type Repository struct {
	ID            string `json:"id"`
	AccountID     string `json:"account_id"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}
type RepositoryPage struct {
	Items    []Repository `json:"items"`
	NextPage int          `json:"next_page,omitempty"`
}
type Selection struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
}
type RepositoryProvider interface {
	Repositories(context.Context, string, int) (RepositoryPage, error)
	Repository(context.Context, string, string, string) (Repository, error)
}
type RepositoryStore interface {
	CheckGiteeContext(context.Context, string, uuid.UUID, uuid.UUID) error
	ImportGiteeRepositories(context.Context, string, uuid.UUID, uuid.UUID, []Repository) ([]integration.Imported, error)
}

var componentPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

func ValidComponent(s string) bool { return componentPattern.MatchString(s) && s != "." && s != ".." }

type remoteRepository struct {
	ID        int64  `json:"id"`
	Path      string `json:"path"`
	Namespace struct {
		ID   int64  `json:"id"`
		Path string `json:"path"`
	} `json:"namespace"`
	Private    bool   `json:"private"`
	Branch     string `json:"default_branch"`
	URL        string `json:"html_url"`
	Permission struct {
		Admin bool `json:"admin"`
	} `json:"permission"`
}

func (r remoteRepository) normalized() (Repository, error) {
	if !r.Permission.Admin {
		return Repository{}, scm.ErrUnauthorized
	}
	if r.ID <= 0 || r.Namespace.ID <= 0 || !ValidComponent(r.Path) || !ValidComponent(r.Namespace.Path) || r.Branch == "" || len(r.Branch) > 255 || strings.ContainsAny(r.Branch, "\r\n\x00") || r.URL != identity.GiteeInstance+"/"+r.Namespace.Path+"/"+r.Path {
		return Repository{}, ErrRemote
	}
	return Repository{ID: strconv.FormatInt(r.ID, 10), AccountID: strconv.FormatInt(r.Namespace.ID, 10), Owner: r.Namespace.Path, Name: r.Path, DefaultBranch: r.Branch, Private: r.Private}, nil
}
func (c *Client) Repositories(ctx context.Context, token string, page int) (RepositoryPage, error) {
	if page < 1 || page > 100 {
		return RepositoryPage{}, ErrStale
	}
	var reply []remoteRepository
	path := "/user/repos?" + url.Values{"page": {strconv.Itoa(page)}, "per_page": {"30"}, "sort": {"full_name"}, "direction": {"asc"}}.Encode()
	if err := c.get(ctx, path, token, &reply, 2<<20); err != nil {
		return RepositoryPage{}, err
	}
	if len(reply) > 30 {
		return RepositoryPage{}, ErrRemote
	}
	result := RepositoryPage{Items: []Repository{}}
	for _, raw := range reply {
		if !raw.Permission.Admin || raw.Branch == "" {
			continue
		}
		repo, err := raw.normalized()
		if err != nil {
			return RepositoryPage{}, err
		}
		result.Items = append(result.Items, repo)
	}
	if len(reply) == 30 && page < 100 {
		result.NextPage = page + 1
	}
	return result, nil
}
func (c *Client) Repository(ctx context.Context, token, owner, name string) (Repository, error) {
	if !ValidComponent(owner) || !ValidComponent(name) {
		return Repository{}, ErrStale
	}
	var reply remoteRepository
	if err := c.get(ctx, "/repos/"+owner+"/"+name, token, &reply, 1<<20); err != nil {
		return Repository{}, err
	}
	repo, err := reply.normalized()
	if err != nil {
		return Repository{}, err
	}
	if repo.Owner != owner || repo.Name != name {
		return Repository{}, ErrStale
	}
	return repo, nil
}
func (s *Service) repositoryAccess(ctx context.Context, session string) (*Grant, []byte, error) {
	snap, err := s.Store.GiteeContext(ctx, session, false)
	if err != nil {
		return nil, nil, err
	}
	if snap.Grant == nil {
		return nil, nil, ErrStale
	}
	token, err := s.Access(ctx, snap.Grant.ID)
	if err != nil {
		return nil, nil, err
	}
	clear(token)
	// Refresh may change revision; pin the current grant before external reads.
	grant, _, err := s.Store.GiteeGrant(ctx, snap.Grant.ID)
	if err != nil {
		return nil, nil, err
	}
	if grant.Status != "active" {
		return nil, nil, ErrBusy
	}
	plain, err := s.cipher.Open(grant.Encrypted, GrantAAD(grant))
	if err != nil {
		return nil, nil, ErrStale
	}
	defer clear(plain)
	var current Token
	if json.Unmarshal(plain, &current) != nil || !validToken(current.Access) {
		return nil, nil, ErrStale
	}
	return &grant, []byte(current.Access), nil
}
func (s *Service) Repositories(ctx context.Context, session string, page int) (RepositoryPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	provider, ok := s.Provider.(RepositoryProvider)
	if !ok {
		return RepositoryPage{}, ErrRemote
	}
	store, ok := s.Store.(RepositoryStore)
	if !ok {
		return RepositoryPage{}, ErrStale
	}
	grant, token, err := s.repositoryAccess(ctx, session)
	if err != nil {
		return RepositoryPage{}, err
	}
	defer clear(token)
	result, err := provider.Repositories(ctx, string(token), page)
	if err != nil {
		return RepositoryPage{}, err
	}
	if err := store.CheckGiteeContext(ctx, session, grant.ID, grant.Revision); err != nil {
		return RepositoryPage{}, err
	}
	return result, nil
}
func (s *Service) Import(ctx context.Context, session string, selected []Selection) ([]integration.Imported, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if len(selected) == 0 || len(selected) > 20 {
		return nil, ErrStale
	}
	seen := map[string]bool{}
	for _, item := range selected {
		if !identity.ValidGitHubSubject(item.ID) || !ValidComponent(item.Owner) || !ValidComponent(item.Name) || seen[item.ID] {
			return nil, ErrStale
		}
		seen[item.ID] = true
	}
	provider, ok := s.Provider.(RepositoryProvider)
	if !ok {
		return nil, ErrRemote
	}
	store, ok := s.Store.(RepositoryStore)
	if !ok {
		return nil, ErrStale
	}
	grant, token, err := s.repositoryAccess(ctx, session)
	if err != nil {
		return nil, err
	}
	defer clear(token)
	repos := make([]Repository, 0, len(selected))
	for _, item := range selected {
		repo, err := provider.Repository(ctx, string(token), item.Owner, item.Name)
		if err != nil {
			return nil, err
		}
		if repo.ID != item.ID {
			return nil, ErrStale
		}
		repos = append(repos, repo)
	}
	return store.ImportGiteeRepositories(ctx, session, grant.ID, grant.Revision, repos)
}
