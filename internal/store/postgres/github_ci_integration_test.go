package postgres

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/githubci"
	"github.com/yuanci/yuanci/internal/githubhook"
	"github.com/yuanci/yuanci/internal/pipeline"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/scm"
)

const githubCIPipeline = `version: v1
name: verify
stages:
  - name: test
    jobs:
      - name: unit
        image: alpine:3.23
        steps:
          - name: test
            commands: ["true"]
`

func claimedGitHubDelivery(t *testing.T, store *Store, externalID string) githubhook.WorkItem {
	t.Helper()
	event := scm.Event{Provider: scm.GitHub, DeliveryID: uuid.NewString(), Type: scm.EventPush,
		Repository: scm.Repository{ExternalID: externalID, Owner: "remote", Name: "untrusted"},
		Ref:        "refs/heads/main", AfterSHA: "0123456789abcdef0123456789abcdef01234567",
		Sender: "octocat", ReceivedAt: time.Now().UTC()}
	normalized, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ReceiveWebhook(t.Context(), githubhook.Delivery{
		Provider: "github", ProviderInstance: "https://github.com", DeliveryID: event.DeliveryID,
		EventType: "push", PayloadSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		NormalizedEvent: normalized, ReceivedAt: event.ReceivedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.ClaimWebhook(t.Context(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return *item
}

func TestCommitWebhookRunIsAtomicAndIdempotent(t *testing.T) {
	store, service, session, _ := importFixture(t)
	authorizeImport(t, service, session.Token)
	imported, err := service.Import(t.Context(), session.Token, "34", []string{"70"})
	if err != nil {
		t.Fatal(err)
	}
	repositoryID := imported[0].ID
	settings, err := store.RuntimeAutomation(t.Context(), repositoryID)
	if err != nil || settings.Enabled {
		t.Fatalf("runtime defaults: %#v %v", settings, err)
	}
	resolvedID, settings, err := store.RuntimeAutomationForGitHub(t.Context(), "70")
	if err != nil || resolvedID != repositoryID || settings.Enabled {
		t.Fatalf("GitHub runtime defaults: id=%s settings=%#v err=%v", resolvedID, settings, err)
	}
	if _, err := store.pool.Exec(t.Context(), `UPDATE repositories SET active=false WHERE id=$1`, repositoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RuntimeAutomation(t.Context(), repositoryID); !errors.Is(err, githubci.ErrRepositoryUnavailable) {
		t.Fatalf("inactive repository automation was available: %v", err)
	}
	if _, _, err := store.RuntimeAutomationForGitHub(t.Context(), "70"); !errors.Is(err, githubci.ErrRepositoryUnavailable) {
		t.Fatalf("inactive GitHub repository automation was available: %v", err)
	}
	if _, err := store.pool.Exec(t.Context(), `UPDATE repositories SET active=true WHERE id=$1`, repositoryID); err != nil {
		t.Fatal(err)
	}
	item := claimedGitHubDelivery(t, store, "70")
	plan, err := pipeline.Compile([]byte(githubCIPipeline), item.Event.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	request := githubci.RunCommit{Delivery: item, RepositoryID: repositoryID, PipelinePath: ".yuanci.yml",
		PipelineSource: []byte(githubCIPipeline), Plan: plan, CreatedAt: item.Event.ReceivedAt}
	result, err := store.CommitWebhookRun(t.Context(), request)
	if err != nil || !result.Created || result.ID == uuid.Nil {
		t.Fatalf("first commit: %#v %v", result, err)
	}
	var deliveries, runs, jobs, definitions, versions, audits int
	var linkedRepository, linkedRun uuid.UUID
	if err := store.pool.QueryRow(t.Context(), `SELECT repository_id,run_id FROM webhook_deliveries WHERE id=$1`, item.ID).
		Scan(&linkedRepository, &linkedRun); err != nil {
		t.Fatal(err)
	}
	for query, target := range map[string]*int{
		`SELECT count(*) FROM webhook_deliveries WHERE status='processed'`:     &deliveries,
		`SELECT count(*) FROM runs WHERE idempotency_key=$1`:                   &runs,
		`SELECT count(*) FROM jobs WHERE run_id=$1`:                            &jobs,
		`SELECT count(*) FROM pipeline_definitions WHERE repository_id=$1`:     &definitions,
		`SELECT count(*) FROM pipeline_versions`:                               &versions,
		`SELECT count(*) FROM audit_events WHERE action='webhook.run_created'`: &audits,
	} {
		args := []any{}
		if query == `SELECT count(*) FROM runs WHERE idempotency_key=$1` {
			args = []any{"github-webhook:" + item.ID.String()}
		} else if query == `SELECT count(*) FROM jobs WHERE run_id=$1` {
			args = []any{result.ID}
		} else if query == `SELECT count(*) FROM pipeline_definitions WHERE repository_id=$1` {
			args = []any{repositoryID}
		}
		if err := store.pool.QueryRow(t.Context(), query, args...).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if linkedRepository != repositoryID || linkedRun != result.ID || deliveries != 1 || runs != 1 || jobs != 1 || definitions != 1 || versions != 1 || audits != 1 {
		t.Fatalf("partial commit: repository=%s run=%s counts=%d/%d/%d/%d/%d/%d", linkedRepository, linkedRun,
			deliveries, runs, jobs, definitions, versions, audits)
	}
	newLease := uuid.New()
	if _, err := store.pool.Exec(t.Context(), `UPDATE webhook_deliveries SET status='processing',processed_at=NULL,
		lease_owner=$2,lease_expires_at=clock_timestamp()+interval '1 minute' WHERE id=$1`, item.ID, newLease); err != nil {
		t.Fatal(err)
	}
	request.Delivery.LeaseID = newLease
	repeated, err := store.CommitWebhookRun(t.Context(), request)
	if err != nil || repeated.Created || repeated.ID != result.ID {
		t.Fatalf("idempotent commit: %#v %v", repeated, err)
	}
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE run_id=$1`, result.ID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("duplicate jobs: %d %v", jobs, err)
	}
	thirdLease := uuid.New()
	if _, err := store.pool.Exec(t.Context(), `UPDATE webhook_deliveries SET status='processing',processed_at=NULL,
		lease_owner=$2,lease_expires_at=clock_timestamp()+interval '1 minute' WHERE id=$1`, item.ID, thirdLease); err != nil {
		t.Fatal(err)
	}
	request.Delivery.LeaseID = thirdLease
	request.Plan.ConfigSHA256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if _, err := store.CommitWebhookRun(t.Context(), request); !errors.Is(err, githubci.ErrInvalidCommit) {
		t.Fatalf("conflicting replay accepted: %v", err)
	}
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM pipeline_versions`).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("conflicting replay changed versions: %d %v", versions, err)
	}
}

func TestSourceRunRequiresProtocolTwoRunner(t *testing.T) {
	store, service, session, _ := importFixture(t)
	authorizeImport(t, service, session.Token)
	imported, err := service.Import(t.Context(), session.Token, "34", []string{"70"})
	if err != nil {
		t.Fatal(err)
	}
	item := claimedGitHubDelivery(t, store, "70")
	plan, err := pipeline.Compile([]byte(githubCIPipeline), item.Event.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CommitWebhookRun(t.Context(), githubci.RunCommit{Delivery: item,
		RepositoryID: imported[0].ID, PipelinePath: ".yuanci.yml", PipelineSource: []byte(githubCIPipeline),
		Plan: plan, CreatedAt: item.Event.ReceivedAt})
	if err != nil || !created.Created {
		t.Fatalf("create source run: %#v %v", created, err)
	}

	poolID := uuid.New()
	if _, err := store.pool.Exec(t.Context(), `INSERT INTO runner_pools(id,name,pool_type)
		VALUES ($1,$2,'standard')`, poolID, "source-protocol-pool"); err != nil {
		t.Fatal(err)
	}
	insertRunner := func(protocol int) uuid.UUID {
		runnerID := uuid.New()
		_, insertErr := store.pool.Exec(t.Context(), `INSERT INTO runners
			(id,pool_id,name,status,capacity,labels,certificate_serial,os,architecture,executor,
			 isolation_level,available_disk_bytes,protocol_version,runner_version)
			VALUES ($1,$2,$3,'online',1,'{}'::jsonb,$4,'linux','amd64','docker','standard',$5,$6,$7)`,
			runnerID, poolID, "source-runner-"+runnerID.String(), "serial-"+runnerID.String(), int64(4<<30),
			protocol, fmt.Sprintf("protocol-v%d", protocol))
		if insertErr != nil {
			t.Fatal(insertErr)
		}
		return runnerID
	}
	legacy := insertRunner(1)
	if assignment, err := store.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: legacy}); err != nil || assignment != nil {
		t.Fatalf("protocol 1 received source job: %#v %v", assignment, err)
	}
	current := insertRunner(2)
	assignment, err := store.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: current})
	if err != nil || assignment == nil || assignment.Source == nil {
		t.Fatalf("protocol 2 source claim: %#v %v", assignment, err)
	}
	if assignment.Source.Provider != "github" || assignment.Source.RepositoryID != "70" ||
		assignment.Source.CommitSHA != item.Event.AfterSHA || !strings.HasPrefix(assignment.Source.CloneURL, "https://") {
		t.Fatalf("untrusted source descriptor: %#v", assignment.Source)
	}
}

func TestCommitWebhookRunRejectsLeaseAndRollsBackAuditFailure(t *testing.T) {
	store, service, session, _ := importFixture(t)
	authorizeImport(t, service, session.Token)
	imported, err := service.Import(t.Context(), session.Token, "34", []string{"70"})
	if err != nil {
		t.Fatal(err)
	}
	item := claimedGitHubDelivery(t, store, "70")
	plan, _ := pipeline.Compile([]byte(githubCIPipeline), item.Event.ReceivedAt)
	request := githubci.RunCommit{Delivery: item, RepositoryID: imported[0].ID, PipelinePath: ".yuanci.yml",
		PipelineSource: []byte(githubCIPipeline), Plan: plan, CreatedAt: item.Event.ReceivedAt}
	request.Delivery.LeaseID = uuid.New()
	if _, err := store.CommitWebhookRun(t.Context(), request); !errors.Is(err, githubhook.ErrLeaseInvalid) {
		t.Fatalf("wrong lease accepted: %v", err)
	}
	request.Delivery.LeaseID = item.LeaseID
	rejectAudit(t, store)
	if _, err := store.CommitWebhookRun(t.Context(), request); err == nil {
		t.Fatal("audit failure committed run")
	}
	var runs, definitions int
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM pipeline_definitions`).Scan(&definitions); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || definitions != 0 {
		t.Fatalf("transaction leaked rows: runs=%d definitions=%d", runs, definitions)
	}
}
