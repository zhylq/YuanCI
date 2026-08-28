package postgres

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/provisioning"
	"github.com/yuanci/yuanci/internal/secrets"
)

func managedFixture(t *testing.T) (*Store, *provisioning.Service, string) {
	t.Helper()
	s, err := Open(t.Context(), newTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	cipher, err := secrets.NewCipher([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	service := provisioning.New(s, cipher, "https://ci.example.test")
	code := identity.NewToken()
	if err := s.IssueSetupCode(t.Context(), code); err != nil {
		t.Fatal(err)
	}
	setup, err := service.Exchange(t.Context(), code)
	if err != nil {
		t.Fatal(err)
	}
	return s, service, setup
}
func candidateInput(active *uuid.UUID) provisioning.Input {
	return provisioning.Input{ClientID: "Iv1.fixture", ClientSecret: "fixture-secret-1234567890", BootstrapSubject: "100", ExpectedActive: active}
}
func candidateTicket(t *testing.T, s *Store, service *provisioning.Service, id uuid.UUID, access provisioning.Access) string {
	t.Helper()
	state, nonce := identity.NewToken(), identity.NewToken()
	if _, err := service.Start(t.Context(), state, nonce, "", id, access); err != nil {
		t.Fatal(err)
	}
	ticket, err := s.ConsumeOAuth(t.Context(), state, nonce)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}
func initializeManaged(t *testing.T, s *Store, service *provisioning.Service, setup string) (identity.Credentials, uuid.UUID) {
	t.Helper()
	access := provisioning.Access{SetupToken: setup}
	id, err := service.Save(t.Context(), access, candidateInput(nil))
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := s.FinishManagedOAuth(t.Context(), candidateTicket(t, s, service, id, access), githubUser("100"), "", setup)
	if err != nil {
		t.Fatal(err)
	}
	return credentials, id
}
func TestSetupCodeOneUseRotationAndExpiry(t *testing.T) {
	s, service, setup := managedFixture(t)
	code := identity.NewToken()
	if err := s.IssueSetupCode(t.Context(), code); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoginSettings(t.Context(), provisioning.Access{SetupToken: setup}); !errors.Is(err, provisioning.ErrSetup) {
		t.Fatal("rotated setup session accepted")
	}
	var wg sync.WaitGroup
	results := make(chan error, 10)
	for range 10 {
		wg.Go(func() { _, err := service.Exchange(t.Context(), code); results <- err })
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, provisioning.ErrSetup) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("code redeemed %d times", success)
	}
	if err := s.IssueSetupCode(t.Context(), code); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE auth_setup SET code_expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Exchange(t.Context(), code); !errors.Is(err, provisioning.ErrSetup) {
		t.Fatal("expired code accepted")
	}
}
func TestManagedConfigEncryptionBootstrapAndClosure(t *testing.T) {
	s, service, setup := managedFixture(t)
	access := provisioning.Access{SetupToken: setup}
	id, err := service.Save(t.Context(), access, candidateInput(nil))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := s.LoginSettings(t.Context(), access)
	if err != nil || settings.Active != nil || settings.Candidate == nil {
		t.Fatal("candidate prematurely active")
	}
	encoded, _ := json.Marshal(settings)
	var stored string
	if err := s.pool.QueryRow(t.Context(), `SELECT encrypted_secret::text FROM login_configs WHERE id=$1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored+string(encoded), candidateInput(nil).ClientSecret) || strings.Contains(string(encoded), "encrypted") {
		t.Fatal("secret exposed")
	}
	ticket := candidateTicket(t, s, service, id, access)
	if _, err := s.FinishManagedOAuth(t.Context(), ticket, githubUser("200"), "", setup); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("non-designated administrator accepted")
	}
	if _, err := s.FinishManagedOAuth(t.Context(), ticket, githubUser("100"), "", identity.NewToken()); !errors.Is(err, provisioning.ErrSetup) {
		t.Fatal("different setup browser accepted")
	}
	credentials, err := s.FinishManagedOAuth(t.Context(), ticket, githubUser("100"), "", setup)
	if err != nil {
		t.Fatal(err)
	}
	status, err := s.ProvisioningStatus(t.Context())
	if err != nil || !status.Initialized || !status.Configured {
		t.Fatal("initialization incomplete")
	}
	if err := s.IssueSetupCode(t.Context(), identity.NewToken()); !errors.Is(err, provisioning.ErrSetup) {
		t.Fatal("setup reopened")
	}
	if _, err := s.LoginSettings(t.Context(), access); !errors.Is(err, provisioning.ErrSetup) {
		t.Fatal("setup credential survived activation")
	}
	if _, err := s.FinishManagedOAuth(t.Context(), ticket, githubUser("100"), "", setup); !errors.Is(err, identity.ErrOAuthFlow) {
		t.Fatal("completion replayed")
	}
	settings, err = s.LoginSettings(t.Context(), provisioning.Access{SessionToken: credentials.Token})
	if err != nil || settings.Active.ID != id {
		t.Fatal("administrator cannot read active metadata")
	}
}
func TestManagedReplacementChecksOwnerPermissionsAndRevision(t *testing.T) {
	s, service, setup := managedFixture(t)
	admin, active := initializeManaged(t, s, service, setup)
	member := oauthLogin(t, s, "200")
	if _, err := service.Save(t.Context(), provisioning.Access{SessionToken: member.Token}, candidateInput(&active)); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("member wrote login settings")
	}
	access := provisioning.Access{SessionToken: admin.Token}
	if _, err := service.Save(t.Context(), access, candidateInput(nil)); !errors.Is(err, provisioning.ErrConflict) {
		t.Fatal("stale revision accepted")
	}
	id, err := service.Save(t.Context(), access, candidateInput(&active))
	if err != nil {
		t.Fatal(err)
	}
	ticket := candidateTicket(t, s, service, id, access)
	if _, err := s.FinishManagedOAuth(t.Context(), ticket, githubUser("200"), admin.Token, ""); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("other identity activated config")
	}
	settings, err := s.LoginSettings(t.Context(), access)
	if err != nil || settings.Active.ID != active {
		t.Fatal("failed verification changed active config")
	}
	state, nonce := identity.NewToken(), identity.NewToken()
	if _, err := service.Start(t.Context(), state, nonce, "", uuid.Nil, provisioning.Access{}); err != nil {
		t.Fatal(err)
	}
	oldTicket, err := s.ConsumeOAuth(t.Context(), state, nonce)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := s.FinishManagedOAuth(t.Context(), ticket, githubUser("100"), admin.Token, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishManagedOAuth(t.Context(), oldTicket, githubUser("100"), "", ""); !errors.Is(err, provisioning.ErrConflict) {
		t.Fatal("retired config flow still accepted")
	}
	if _, err := s.AuthenticateSession(t.Context(), admin.Token); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("session not rotated")
	}
	access = provisioning.Access{SessionToken: replacement.Token}
	id2, err := service.Save(t.Context(), access, candidateInput(&id))
	if err != nil {
		t.Fatal(err)
	}
	ticket = candidateTicket(t, s, service, id2, access)
	if _, err := s.pool.Exec(t.Context(), `DELETE FROM memberships WHERE user_id=$1`, replacement.Session.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishManagedOAuth(t.Context(), ticket, githubUser("100"), replacement.Token, ""); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("revoked administrator activated config")
	}
}
func TestManagedActivationAuditFailureRollsBack(t *testing.T) {
	s, service, setup := managedFixture(t)
	access := provisioning.Access{SetupToken: setup}
	id, err := service.Save(t.Context(), access, candidateInput(nil))
	if err != nil {
		t.Fatal(err)
	}
	ticket := candidateTicket(t, s, service, id, access)
	_, err = s.pool.Exec(t.Context(), `CREATE FUNCTION reject_activation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
        IF NEW.action='login_config.activated' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$;
        CREATE TRIGGER reject_activation BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_activation();`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishManagedOAuth(t.Context(), ticket, githubUser("100"), "", setup); err == nil {
		t.Fatal("audit failure ignored")
	}
	for _, table := range []string{"users", "memberships", "browser_sessions", "oauth_bootstrap"} {
		var count int
		if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s not rolled back", table)
		}
	}
	settings, err := s.LoginSettings(t.Context(), access)
	if err != nil || settings.Active != nil || settings.Candidate == nil {
		t.Fatal("setup state not rolled back")
	}
}
func TestManagedCandidatePinningAndExpiredSession(t *testing.T) {
	s, service, setup := managedFixture(t)
	access := provisioning.Access{SetupToken: setup}
	id, err := service.Save(t.Context(), access, candidateInput(nil))
	if err != nil {
		t.Fatal(err)
	}
	ticket := candidateTicket(t, s, service, id, access)
	if _, err := service.Save(t.Context(), access, candidateInput(nil)); err != nil {
		t.Fatal(err)
	}
	config, err := s.ManagedFlowConfig(t.Context(), ticket)
	if err != nil || config.ID != id {
		t.Fatal("flow changed configuration revision")
	}
	if _, err := s.FinishManagedOAuth(t.Context(), ticket, githubUser("100"), "", setup); !errors.Is(err, provisioning.ErrConflict) {
		t.Fatal("superseded candidate activated")
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE auth_setup SET session_expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Save(t.Context(), access, candidateInput(nil)); !errors.Is(err, provisioning.ErrSetup) {
		t.Fatal("expired setup session accepted")
	}
}

func TestManagedActivationRaceAndMasterKeyBinding(t *testing.T) {
	s, service, setup := managedFixture(t)
	key := []byte(strings.Repeat("k", 32))
	if err := s.BindManagedMasterKey(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if err := s.BindManagedMasterKey(t.Context(), key); err != nil {
		t.Fatal("same key rejected")
	}
	if err := s.BindManagedMasterKey(t.Context(), []byte(strings.Repeat("x", 32))); !errors.Is(err, provisioning.ErrConfig) {
		t.Fatal("changed key accepted")
	}
	access := provisioning.Access{SetupToken: setup}
	id, err := service.Save(t.Context(), access, candidateInput(nil))
	if err != nil {
		t.Fatal(err)
	}
	ticket := candidateTicket(t, s, service, id, access)
	var wg sync.WaitGroup
	results := make(chan error, 10)
	for range 10 {
		wg.Go(func() {
			_, err := s.FinishManagedOAuth(t.Context(), ticket, githubUser("100"), "", setup)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, identity.ErrOAuthFlow) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal("activation committed more than once")
	}
}
