package postgres

import (
	"errors"
	"testing"

	"github.com/yuanci/yuanci/internal/githubapp"
)

func TestResolveGitHubRepositoryUsesActiveImportedBinding(t *testing.T) {
	store, service, session, _ := importFixture(t)
	authorizeImport(t, service, session.Token)
	items, err := service.Import(t.Context(), session.Token, "34", []string{"70"})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := store.ResolveGitHubRepository(t.Context(), "70")
	if err != nil || repository.ID != items[0].ID || repository.ExternalID != "70" || repository.InstallationID != "34" ||
		repository.Owner != "team" || repository.Name != "safe" || repository.AppID.String() == "" || len(repository.EncryptedKey.Ciphertext) == 0 {
		t.Fatalf("resolved repository: %#v %v", repository, err)
	}
	if _, err := store.ResolveGitHubRepository(t.Context(), "../70"); !errors.Is(err, githubapp.ErrRepositoryUnavailable) {
		t.Fatal("unsafe external ID resolved")
	}
	if _, err := store.pool.Exec(t.Context(), `UPDATE repositories SET active=false WHERE id=$1`, repository.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveGitHubRepository(t.Context(), "70"); !errors.Is(err, githubapp.ErrRepositoryUnavailable) {
		t.Fatal("inactive repository resolved")
	}
}
