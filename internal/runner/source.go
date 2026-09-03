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
	if source == nil || source.Provider != "github" || !validGitHubRepositoryID(source.RepositoryId) ||
		!commitSHAPattern.MatchString(source.CommitSha) || !validGitHubCloneURL(source.CloneUrl) {
		return errors.New("invalid Runner source descriptor")
	}
	return nil
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
