package postgres

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/runnerauth"
)

func TestRunnerIdentityEnrollmentRotationRevocationAndAudit(t *testing.T) {
	store, err := Open(t.Context(), newTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	var standardPool uuid.UUID
	if err := store.pool.QueryRow(t.Context(), `SELECT id FROM runner_pools WHERE name='standard' AND pool_type='standard'`).Scan(&standardPool); err != nil {
		t.Fatal(err)
	}
	privilegedPool := uuid.New()
	if _, err := store.pool.Exec(t.Context(), `INSERT INTO runner_pools(id,name,pool_type) VALUES
        ($1,'privileged','privileged')`, privilegedPool); err != nil {
		t.Fatal(err)
	}

	t.Run("one-use token is consumed once under concurrency", func(t *testing.T) {
		token := registrationRecord("standard", "one-use", time.Now().Add(time.Hour))
		if err := store.CreateRegistrationToken(t.Context(), token); err != nil {
			t.Fatal(err)
		}
		const attempts = 8
		var wait sync.WaitGroup
		results := make(chan error, attempts)
		for index := range attempts {
			wait.Go(func() {
				enrollment := enrollmentRecord(index+1, token.Digest, "concurrent-"+fmt.Sprint(index))
				_, err := store.EnrollRunner(t.Context(), enrollment)
				results <- err
			})
		}
		wait.Wait()
		close(results)
		successes := 0
		for err := range results {
			if err == nil {
				successes++
			} else if !errors.Is(err, runnerauth.ErrDenied) {
				t.Fatalf("unexpected enrollment error: %v", err)
			}
		}
		if successes != 1 {
			t.Fatalf("one-use token enrolled %d Runners", successes)
		}
		var used int
		if err := store.pool.QueryRow(t.Context(), `SELECT used_count FROM runner_registration_tokens WHERE id=$1`, token.ID).Scan(&used); err != nil || used != 1 {
			t.Fatalf("token use count=%d err=%v", used, err)
		}
	})

	t.Run("expired and wrong-pool tokens are denied", func(t *testing.T) {
		expiredDigest := sha256.Sum256([]byte("expired-token"))
		if _, err := store.pool.Exec(t.Context(), `INSERT INTO runner_registration_tokens
            (id,pool_id,token_digest,created_at,expires_at) VALUES($1,$2,$3,clock_timestamp()-interval '2 hours',clock_timestamp()-interval '1 hour')`,
			uuid.New(), standardPool, expiredDigest[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := store.EnrollRunner(t.Context(), enrollmentRecord(20, expiredDigest, "expired")); !errors.Is(err, runnerauth.ErrDenied) {
			t.Fatal("expired token accepted")
		}
		wrong := registrationRecord("standard", "wrong-pool", time.Now().Add(time.Hour))
		if err := store.CreateRegistrationToken(t.Context(), wrong); err != nil {
			t.Fatal(err)
		}
		enrollment := enrollmentRecord(21, wrong.Digest, "wrong-pool")
		enrollment.Capabilities.IsolationLevel = "privileged"
		if _, err := store.EnrollRunner(t.Context(), enrollment); !errors.Is(err, runnerauth.ErrDenied) {
			t.Fatal("token enrolled Runner into a different pool type")
		}
	})

	t.Run("audit failure rolls enrollment back", func(t *testing.T) {
		token := registrationRecord("standard", "audit-failure", time.Now().Add(time.Hour))
		if err := store.CreateRegistrationToken(t.Context(), token); err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(t.Context(), `CREATE FUNCTION reject_runner_enroll_audit() RETURNS trigger LANGUAGE plpgsql AS $$
            BEGIN IF NEW.action='runner.enrolled' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END; $$;
            CREATE TRIGGER reject_runner_enroll_audit BEFORE INSERT ON audit_events
            FOR EACH ROW EXECUTE FUNCTION reject_runner_enroll_audit();`); err != nil {
			t.Fatal(err)
		}
		enrollment := enrollmentRecord(30, token.Digest, "audit-rollback")
		if _, err := store.EnrollRunner(t.Context(), enrollment); err == nil {
			t.Fatal("audit failure ignored")
		}
		var runners, certificates, uses int
		_ = store.pool.QueryRow(t.Context(), `SELECT count(*) FROM runners WHERE id=$1`, enrollment.RunnerID).Scan(&runners)
		_ = store.pool.QueryRow(t.Context(), `SELECT count(*) FROM runner_certificates WHERE runner_id=$1`, enrollment.RunnerID).Scan(&certificates)
		_ = store.pool.QueryRow(t.Context(), `SELECT used_count FROM runner_registration_tokens WHERE id=$1`, token.ID).Scan(&uses)
		if runners != 0 || certificates != 0 || uses != 0 {
			t.Fatalf("partial enrollment committed: runners=%d certificates=%d uses=%d", runners, certificates, uses)
		}
		if _, err := store.pool.Exec(t.Context(), `DROP TRIGGER reject_runner_enroll_audit ON audit_events; DROP FUNCTION reject_runner_enroll_audit()`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("rotation is idempotent and identities are immediately revocable", func(t *testing.T) {
		token := registrationRecord("standard", "rotation", time.Now().Add(time.Hour))
		if err := store.CreateRegistrationToken(t.Context(), token); err != nil {
			t.Fatal(err)
		}
		enrollment := enrollmentRecord(40, token.Digest, "rotation-runner")
		identity, err := store.EnrollRunner(t.Context(), enrollment)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AuthenticateRunner(t.Context(), uuid.New(), identity.Serial); !errors.Is(err, runnerauth.ErrDenied) {
			t.Fatal("spoofed Runner URI identity authenticated")
		}
		if _, err := store.AuthenticateRunner(t.Context(), identity.RunnerID, identity.Serial); err != nil {
			t.Fatal(err)
		}
		replacement := certificateRecordFor(41)
		rotation := runnerauth.Rotation{RunnerID: identity.RunnerID, OldSerial: identity.Serial,
			Certificate: replacement, GracePeriod: runnerauth.RotationGracePeriod}
		first, err := store.RotateRunnerCertificate(t.Context(), rotation)
		if err != nil {
			t.Fatal(err)
		}
		rotation.Certificate = certificateRecordFor(42)
		rotation.Certificate.CSRFingerprint = replacement.CSRFingerprint
		rotation.Certificate.PublicKeyFingerprint = replacement.PublicKeyFingerprint
		second, err := store.RotateRunnerCertificate(t.Context(), rotation)
		if err != nil || second.Serial != first.Serial || string(second.ChainPEM) != string(first.ChainPEM) {
			t.Fatal("lost rotation response did not return the same certificate")
		}
		unrelated := rotation
		unrelated.Certificate = certificateRecordFor(43)
		if _, err := store.RotateRunnerCertificate(t.Context(), unrelated); !errors.Is(err, runnerauth.ErrDenied) {
			t.Fatal("unrelated retry CSR accepted")
		}
		if _, err := store.AuthenticateRunner(t.Context(), identity.RunnerID, identity.Serial); err != nil {
			t.Fatal("old certificate rejected inside grace period")
		}
		if _, err := store.pool.Exec(t.Context(), `UPDATE runner_certificates SET retire_at=clock_timestamp()-interval '1 second' WHERE serial=$1`, identity.Serial); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AuthenticateRunner(t.Context(), identity.RunnerID, identity.Serial); !errors.Is(err, runnerauth.ErrDenied) {
			t.Fatal("old certificate authenticated after grace period")
		}
		if err := store.RevokeRunnerCertificate(t.Context(), first.Serial, "operator_revoke", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AuthenticateRunner(t.Context(), identity.RunnerID, first.Serial); !errors.Is(err, runnerauth.ErrDenied) {
			t.Fatal("revoked certificate authenticated")
		}
		if err := store.DisableRunner(t.Context(), identity.RunnerID, "maintenance", nil); err != nil {
			t.Fatal(err)
		}
		var audits int
		if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE resource_id=$1
            AND action IN ('runner.enrolled','runner_certificate.rotated','runner_certificate.revoked','runner.disabled')`, identity.RunnerID.String()).Scan(&audits); err != nil || audits != 4 {
			t.Fatalf("Runner audit count=%d err=%v", audits, err)
		}
	})

	t.Run("database contains no plaintext token or private key", func(t *testing.T) {
		plaintext := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		digest, _ := runnerauth.TokenDigest(plaintext)
		record := runnerauth.RegistrationToken{ID: uuid.New(), PoolName: "standard", Digest: digest,
			ExpiresAt: time.Now().Add(time.Hour), MaxUses: 1}
		if err := store.CreateRegistrationToken(t.Context(), record); err != nil {
			t.Fatal(err)
		}
		var leaked bool
		pattern := "%" + plaintext + "%"
		if err := store.pool.QueryRow(t.Context(), `SELECT EXISTS(
            SELECT 1 FROM runner_registration_tokens WHERE encode(token_digest,'escape') LIKE $1
            UNION ALL SELECT 1 FROM audit_events WHERE metadata::text LIKE $1
            UNION ALL SELECT 1 FROM runner_certificates WHERE convert_from(certificate_chain_pem,'UTF8') LIKE '%PRIVATE KEY%')`, pattern).Scan(&leaked); err != nil || leaked {
			t.Fatalf("secret material persisted: leaked=%v err=%v", leaked, err)
		}
	})
}

func registrationRecord(pool, seed string, expiry time.Time) runnerauth.RegistrationToken {
	digest := sha256.Sum256([]byte(seed))
	return runnerauth.RegistrationToken{ID: uuid.New(), PoolName: pool, Digest: digest, ExpiresAt: expiry, MaxUses: 1}
}

func enrollmentRecord(index int, digest [32]byte, name string) runnerauth.Enrollment {
	return runnerauth.Enrollment{TokenDigest: digest, RunnerID: uuid.New(), Name: name,
		Capabilities: runnerauth.Capabilities{OS: "linux", Architecture: "amd64", Executor: "docker",
			IsolationLevel: "standard", Labels: map[string]string{"test": "true"}, Capacity: 1,
			AvailableDiskBytes: 1024, ProtocolVersion: 1, RunnerVersion: "integration"},
		Certificate: certificateRecordFor(index)}
}

func certificateRecordFor(index int) runnerauth.CertificateRecord {
	csr := sha256.Sum256([]byte(fmt.Sprintf("csr-%d", index)))
	public := sha256.Sum256([]byte(fmt.Sprintf("public-%d", index)))
	return runnerauth.CertificateRecord{Serial: fmt.Sprintf("%016x", index+1), CSRFingerprint: csr,
		PublicKeyFingerprint: public, ChainPEM: []byte(fmt.Sprintf("PUBLIC CERTIFICATE %d", index)),
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
}
