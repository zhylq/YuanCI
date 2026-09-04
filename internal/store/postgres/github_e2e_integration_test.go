package postgres

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	"github.com/yuanci/yuanci/internal/commitstatus"
	"github.com/yuanci/yuanci/internal/githubapp"
	"github.com/yuanci/yuanci/internal/githubci"
	"github.com/yuanci/yuanci/internal/githubhook"
	"github.com/yuanci/yuanci/internal/project"
	"github.com/yuanci/yuanci/internal/runner"
	"github.com/yuanci/yuanci/internal/runnerauth"
	"github.com/yuanci/yuanci/internal/runnergrpc"
	"github.com/yuanci/yuanci/internal/scm"
	"github.com/yuanci/yuanci/internal/secrets"
)

func TestFakeGitHubEndToEnd(t *testing.T) {
	exerciseFakeGitHubEndToEnd(t)
}

const e2eCheckoutToken = "e2e-private-checkout-token"
const e2eStatusToken = "e2e-status-only-token"
const e2eWebhookSecret = "e2e-webhook-signature-secret"

type fakeGitHub struct {
	repository, sha string
	mu              sync.Mutex
	statuses        []scm.CommitStatus
	issued, keys    [][]byte
	files           int
}

func (p *fakeGitHub) mint(client string, key []byte, installation, repository, token string) ([]byte, time.Time, error) {
	if client != "Iv1.test" || string(key) != "e2e-app-private-key" || installation != "34" || repository != "70" {
		return nil, time.Time{}, errors.New("fake GitHub scope mismatch")
	}
	value := []byte(token)
	p.mu.Lock()
	p.issued = append(p.issued, value)
	p.keys = append(p.keys, key)
	p.mu.Unlock()
	return value, time.Now().Add(time.Hour), nil
}
func (p *fakeGitHub) InstallationToken(_ context.Context, c string, k []byte, i, r string) ([]byte, time.Time, error) {
	return p.mint(c, k, i, r, e2eCheckoutToken)
}
func (p *fakeGitHub) CommitStatusToken(_ context.Context, c string, k []byte, i, r string) ([]byte, time.Time, error) {
	return p.mint(c, k, i, r, e2eStatusToken)
}
func (p *fakeGitHub) RepositoryCommit(context.Context, []byte, string, string, string) (string, error) {
	return p.sha, nil
}
func (p *fakeGitHub) RepositoryFile(ctx context.Context, token []byte, owner, name, path, sha string) ([]byte, error) {
	if string(token) != e2eCheckoutToken || owner != "team" || name != "safe" || path != ".yuanci.yml" || sha != p.sha {
		return nil, errors.New("fake immutable file mismatch")
	}
	p.mu.Lock()
	p.files++
	p.mu.Unlock()
	return exec.CommandContext(ctx, "git", "-C", p.repository, "show", sha+":"+path).Output()
}
func (p *fakeGitHub) SetCommitStatus(_ context.Context, token []byte, owner, name string, status scm.CommitStatus) error {
	if string(token) != e2eStatusToken || owner != "team" || name != "safe" || status.SHA != p.sha {
		return errors.New("fake status binding mismatch")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statuses = append(p.statuses, status)
	return nil
}

type e2eHookSecret struct{}

func (e2eHookSecret) WebhookSecret(context.Context) ([]byte, error) {
	return []byte(e2eWebhookSecret), nil
}

func exerciseFakeGitHubEndToEnd(t *testing.T) {
	f := newAccessFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture Git failed: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main", repository)
	config := strings.Replace(githubCIPipeline, `commands: ["true"]`, `commands: ["cat proof.txt"]`, 1)
	if err := os.WriteFile(filepath.Join(repository, ".yuanci.yml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "proof.txt"), []byte("immutable-private-source-executed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", repository, "add", ".")
	git("-C", repository, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "immutable fixture")
	sha := git("-C", repository, "rev-parse", "HEAD")
	// Move the branch after capture: config/checkout must still use the event SHA.
	if err := os.WriteFile(filepath.Join(repository, "proof.txt"), []byte("wrong-moving-branch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", repository, "add", ".")
	git("-C", repository, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "move branch")
	t.Setenv("YUANCI_E2E_DOCKER_ROOT", root)
	t.Setenv("YUANCI_E2E_SOURCE_SHA", sha)
	appRevision := bindGitHubAutomation(t, f)
	cipher, err := secrets.NewCipher([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Seal([]byte("e2e-app-private-key"), githubapp.KeyAAD(appRevision))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(encrypted)
	if _, err := f.store.pool.Exec(ctx, `UPDATE github_app_configs SET encrypted_key=$2 WHERE id=$1`, appRevision, encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(ctx, `UPDATE repositories SET owner='team',name='safe',clone_url='https://github.com/team/safe.git' WHERE id=$1`, f.project); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGitHub{repository: repository, sha: sha}
	app, err := githubapp.New(f.store, cipher, fake)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := app.ValidateDefaultPipeline(ctx, "70", project.DefaultPipelinePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.RecordProjectAutomationValidation(ctx, f.adminSession.Token, project.AutomationValidation{RepositoryID: f.project, AppRevision: appRevision, PipelinePath: project.DefaultPipelinePath, CommitSHA: proof.CommitSHA, ConfigSHA256: proof.ConfigSHA256, PipelineName: proof.PipelineName, ValidatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	update := automationUpdate(0)
	update.Enabled = true
	update.PipelinePath = project.DefaultPipelinePath
	if _, err := f.store.UpdateProjectAutomation(ctx, f.adminSession.Token, f.project, update); err != nil {
		t.Fatal(err)
	}
	hooks, err := githubhook.New(e2eHookSecret{}, f.store)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"ref":"refs/heads/main","before":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","after":"` + sha + `","repository":{"id":70,"name":"safe","owner":{"login":"team"}},"sender":{"login":"fixture"}}`)
	headers := http.Header{"X-Github-Event": []string{"push"}, "X-Github-Delivery": []string{uuid.NewString()}}
	mac := hmac.New(sha256.New, []byte(e2eWebhookSecret))
	_, _ = mac.Write(payload)
	headers.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	if _, err := hooks.Receive(ctx, headers, append(payload, ' ')); err == nil {
		t.Fatal("invalid signature accepted")
	}
	receipt, err := hooks.Receive(ctx, headers, payload)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := hooks.Receive(ctx, headers, payload)
	if err != nil || !duplicate.Duplicate || duplicate.ID != receipt.ID {
		t.Fatal("webhook replay duplicated")
	}
	delivery, err := f.store.ClaimWebhook(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator, err := githubci.NewOrchestrator(f.store, app)
	if err != nil {
		t.Fatal(err)
	}
	if outcome, err := orchestrator.Process(ctx, *delivery); err != nil || outcome != githubci.OutcomeRunCreated {
		t.Fatalf("orchestrator: %s %v", outcome, err)
	}
	var runID uuid.UUID
	if err := f.store.pool.QueryRow(ctx, `SELECT run_id FROM webhook_deliveries WHERE id=$1`, receipt.ID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	pkiDir := filepath.Join(root, "pki")
	if _, err := runnerauth.InitializePKI(runnerauth.PKIOptions{OutputDir: pkiDir, ServerNames: []string{"server", "127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	pki, err := runnergrpc.LoadPKI(runnergrpc.PKIFiles{ServerCertificate: filepath.Join(pkiDir, "server", "server-chain.pem"), ServerKey: filepath.Join(pkiDir, "server", "server-key.pem"), ClientCA: filepath.Join(pkiDir, "server", "root-cert.pem"), IssuerCertificate: filepath.Join(pkiDir, "server", "intermediate-cert.pem"), IssuerKey: filepath.Join(pkiDir, "server", "intermediate-key.pem")})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := runnerauth.New(f.store, pki.Issuer, pki.IssuerKey)
	if err != nil {
		t.Fatal(err)
	}
	registration, _, err := auth.IssueToken(ctx, "standard", time.Minute, 1, &f.admin)
	if err != nil {
		t.Fatal(err)
	}
	grpcServer, err := runnergrpc.NewServer(auth, f.store, pki.RootPEM, pki.TLSConfig, app)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = grpcServer.Serve(listener) }()
	defer grpcServer.Stop()
	defer listener.Close()
	caps := &runnerv1.RunnerCapabilities{Os: "linux", Architecture: "amd64", Executor: "docker", Capacity: 1, AvailableDiskBytes: 1 << 30, Labels: map[string]string{}, IsolationLevel: runnerv1.IsolationLevel_ISOLATION_LEVEL_STANDARD}
	credentials, err := runner.LoadOrEnroll(ctx, runner.EnrollmentConfig{Address: listener.Addr().String(), ServerName: "server", RootCAFile: filepath.Join(pkiDir, "server", "root-cert.pem"), StateDir: filepath.Join(root, "runner"), Token: registration, Name: "e2e", Capabilities: caps})
	if err != nil {
		t.Fatal(err)
	}
	client, err := runner.NewWorkClient(runner.WorkConfig{Address: listener.Addr().String(), ServerName: "server", Credentials: credentials, Capabilities: caps})
	if err != nil {
		t.Fatal(err)
	}
	var localOut, localErr bytes.Buffer
	executor := runner.NewDockerExecutor(&localOut, &localErr)
	executor.Binary = os.Args[0]
	provider, err := commitstatus.NewGitHubProvider(app)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := commitstatus.NewWorker(f.store, provider, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { worker.Run(workerCtx); close(done) }()
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- client.Run(workerCtx, executor) }()
	var stopOnce sync.Once
	cleanup := func() { stopOnce.Do(func() { stop(); <-done; <-runnerDone }) }
	defer cleanup()
	for {
		var state string
		var delivered int
		if err := f.store.pool.QueryRow(ctx, `SELECT status FROM runs WHERE id=$1`, runID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "failed" {
			t.Fatal("E2E execution failed")
		}
		if err := f.store.pool.QueryRow(ctx, `SELECT count(*) FROM commit_status_outbox WHERE run_id=$1 AND delivery_state='delivered'`, runID).Scan(&delivered); err != nil {
			t.Fatal(err)
		}
		if state == "succeeded" && delivered == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("E2E convergence timed out")
		case <-time.After(20 * time.Millisecond):
		}
	}
	cleanup()
	detail, err := f.store.GetAuthorizedRun(ctx, f.adminSession.Token, f.project, runID)
	if err != nil || len(detail.Jobs) != 1 {
		t.Fatalf("detail: %v", err)
	}
	logs, err := f.store.ReadAuthorizedLogs(ctx, f.adminSession.Token, f.project, runID, detail.Jobs[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	for _, c := range logs.Items {
		output.Write(c.Data)
	}
	if !strings.Contains(output.String(), "immutable-private-source-executed") || !strings.Contains(output.String(), "[REDACTED]") || strings.Contains(output.String(), "wrong-moving-branch") {
		t.Fatal("source identity or redaction failed")
	}
	for _, secret := range []string{e2eCheckoutToken, e2eStatusToken, e2eWebhookSecret, "e2e-app-private-key"} {
		if strings.Contains(output.String()+localOut.String()+localErr.String(), secret) {
			t.Fatal("secret reached output")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "workspace")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("workspace was not cleaned")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.statuses) != 2 || fake.statuses[0].State != "pending" || fake.statuses[1].State != "success" || fake.files != 2 {
		t.Fatalf("provider lifecycle: statuses=%d files=%d", len(fake.statuses), fake.files)
	}
	for _, buffer := range append(fake.issued, fake.keys...) {
		for _, b := range buffer {
			if b != 0 {
				t.Fatal("credential buffer not cleared")
			}
		}
	}
}
