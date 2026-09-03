package runner

import (
	"strings"
	"testing"

	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
)

func TestValidateSourceDescriptor(t *testing.T) {
	valid := &runnerv1.SourceCheckout{Provider: "github", RepositoryId: "70",
		CloneUrl: "https://github.com/example/repository.git", CommitSha: "0123456789abcdef0123456789ABCDEF01234567"}
	if err := validateSourceDescriptor(valid); err != nil {
		t.Fatalf("valid GitHub source rejected: %v", err)
	}

	tests := map[string]func(*runnerv1.SourceCheckout){
		"nil":                 func(source *runnerv1.SourceCheckout) {},
		"provider":            func(source *runnerv1.SourceCheckout) { source.Provider = "gitlab" },
		"repository zero":     func(source *runnerv1.SourceCheckout) { source.RepositoryId = "0" },
		"repository padded":   func(source *runnerv1.SourceCheckout) { source.RepositoryId = "070" },
		"repository signed":   func(source *runnerv1.SourceCheckout) { source.RepositoryId = "+70" },
		"repository overflow": func(source *runnerv1.SourceCheckout) { source.RepositoryId = "9223372036854775808" },
		"http":                func(source *runnerv1.SourceCheckout) { source.CloneUrl = "http://github.com/example/repository.git" },
		"userinfo": func(source *runnerv1.SourceCheckout) {
			source.CloneUrl = "https://token@github.com/example/repository.git"
		},
		"query":    func(source *runnerv1.SourceCheckout) { source.CloneUrl += "?ref=main" },
		"fragment": func(source *runnerv1.SourceCheckout) { source.CloneUrl += "#main" },
		"explicit port": func(source *runnerv1.SourceCheckout) {
			source.CloneUrl = "https://github.com:443/example/repository.git"
		},
		"host suffix": func(source *runnerv1.SourceCheckout) {
			source.CloneUrl = "https://github.com.evil.test/example/repository.git"
		},
		"host trailing dot": func(source *runnerv1.SourceCheckout) { source.CloneUrl = "https://github.com./example/repository.git" },
		"localhost":         func(source *runnerv1.SourceCheckout) { source.CloneUrl = "https://localhost/example/repository.git" },
		"loopback IPv4":     func(source *runnerv1.SourceCheckout) { source.CloneUrl = "https://127.0.0.1/example/repository.git" },
		"loopback IPv6":     func(source *runnerv1.SourceCheckout) { source.CloneUrl = "https://[::1]/example/repository.git" },
		"escaped path":      func(source *runnerv1.SourceCheckout) { source.CloneUrl = "https://github.com/example%2Frepository.git" },
		"backslash":         func(source *runnerv1.SourceCheckout) { source.CloneUrl = `https://github.com/example\repository.git` },
		"extra segment": func(source *runnerv1.SourceCheckout) {
			source.CloneUrl = "https://github.com/example/repository/extra.git"
		},
		"missing git suffix": func(source *runnerv1.SourceCheckout) { source.CloneUrl = "https://github.com/example/repository" },
		"dot owner":          func(source *runnerv1.SourceCheckout) { source.CloneUrl = "https://github.com/./repository.git" },
		"dot repository":     func(source *runnerv1.SourceCheckout) { source.CloneUrl = "https://github.com/example/...git" },
		"invalid owner":      func(source *runnerv1.SourceCheckout) { source.CloneUrl = "https://github.com/-example/repository.git" },
		"invalid repository": func(source *runnerv1.SourceCheckout) { source.CloneUrl = "https://github.com/example/repo%20name.git" },
		"short SHA":          func(source *runnerv1.SourceCheckout) { source.CommitSha = strings.Repeat("a", 39) },
		"long SHA":           func(source *runnerv1.SourceCheckout) { source.CommitSha = strings.Repeat("a", 41) },
		"non-hex SHA":        func(source *runnerv1.SourceCheckout) { source.CommitSha = strings.Repeat("g", 40) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if name == "nil" {
				if err := validateSourceDescriptor(nil); err == nil {
					t.Fatal("nil source accepted")
				}
				return
			}
			source := &runnerv1.SourceCheckout{Provider: valid.Provider, RepositoryId: valid.RepositoryId,
				CloneUrl: valid.CloneUrl, CommitSha: valid.CommitSha}
			mutate(source)
			if err := validateSourceDescriptor(source); err == nil {
				t.Fatalf("invalid source accepted: provider=%q repository_id=%q clone_url=%q commit_sha=%q",
					source.Provider, source.RepositoryId, source.CloneUrl, source.CommitSha)
			}
		})
	}
}

func FuzzValidateSourceDescriptor(f *testing.F) {
	f.Add("github", "70", "https://github.com/example/repository.git", "0123456789abcdef0123456789abcdef01234567")
	f.Add("github", "70", "https://127.0.0.1/example/repository.git", strings.Repeat("a", 40))
	f.Add("github", "070", "https://github.com/example/repository.git?x=1", strings.Repeat("g", 40))
	f.Fuzz(func(t *testing.T, provider, repositoryID, cloneURL, commitSHA string) {
		source := &runnerv1.SourceCheckout{Provider: provider, RepositoryId: repositoryID, CloneUrl: cloneURL, CommitSha: commitSHA}
		if validateSourceDescriptor(source) == nil {
			if provider != "github" || cloneURL != "https://github.com/example/repository.git" &&
				(strings.ContainsAny(cloneURL, "?#@\\\r\n\x00") || !strings.HasPrefix(cloneURL, "https://github.com/")) {
				t.Fatalf("ambiguous source accepted: %#v", source)
			}
		}
	})
}
