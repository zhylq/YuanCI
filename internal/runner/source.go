package runner

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
)

var (
	githubOwnerPattern      = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,99}$`)
	githubRepositoryPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,100}$`)
	commitSHAPattern        = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
)

func validateSourceDescriptor(source *runnerv1.SourceCheckout) error {
	if source == nil || !validGitHubRepositoryID(source.RepositoryId) || !commitSHAPattern.MatchString(source.CommitSha) {
		return errors.New("invalid Runner source descriptor")
	}
	if source.Provider == "github" && validGitHubCloneURL(source.CloneUrl) {
		return nil
	}
	if source.Provider == "gitee" && validGiteeBrokerURL(source.CloneUrl, source.RepositoryId) {
		return nil
	}
	return errors.New("invalid Runner source descriptor")
}

// Broker URLs come only from the authenticated control plane over Runner mTLS,
// never from pipeline YAML or webhook URLs. The token is useless at Gitee itself.
func validGiteeBrokerURL(value, repositoryID string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	return u.Path == "/api/v1/checkout/gitee/"+repositoryID+".git" && value == "https://"+u.Host+u.Path
}

func validGitHubRepositoryID(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

func validGitHubCloneURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil ||
		parsed.Opaque != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(parts) != 2 || !strings.HasSuffix(parts[1], ".git") {
		return false
	}
	owner, repository := parts[0], strings.TrimSuffix(parts[1], ".git")
	if !githubOwnerPattern.MatchString(owner) || !githubRepositoryPattern.MatchString(repository) ||
		repository == "." || repository == ".." {
		return false
	}
	return value == "https://github.com/"+owner+"/"+repository+".git"
}
