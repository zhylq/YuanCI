package postgres

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/project"
)

func bindGitHubAutomation(t *testing.T, f accessFixture) uuid.UUID {
	t.Helper()
	loginID, appRevision := uuid.New(), uuid.New()
	if _, err := f.store.pool.Exec(t.Context(), `INSERT INTO login_configs
		(id,client_id,encrypted_secret,bootstrap_subject,status,created_by)
		VALUES($1,'Iv1.test','{}','1','active',$2)`, loginID, f.admin); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(t.Context(), `INSERT INTO github_app_configs
		(id,login_config_id,app_id,client_id,slug,encrypted_key)
		VALUES($1,$2,99,'Iv1.test','fixture','{}')`, appRevision, loginID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(t.Context(), `INSERT INTO github_accounts(account_id,organization_id) VALUES(12,$1)`, f.organization); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(t.Context(), `INSERT INTO github_installations(id,app_id,account_id) VALUES(34,99,12)`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(t.Context(), `UPDATE repositories SET external_id='70',github_installation_id=34 WHERE id=$1`, f.project); err != nil {
		t.Fatal(err)
	}
	return appRevision
}

func automationValidation(projectID, appRevision uuid.UUID, revision int64) project.AutomationValidation {
	return project.AutomationValidation{RepositoryID: projectID, AppRevision: appRevision, SettingsRevision: revision,
		PipelinePath: project.DefaultPipelinePath, CommitSHA: "0123456789abcdef0123456789abcdef01234567",
		ConfigSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		PipelineName: "validated", ValidatedAt: time.Now().UTC()}
}

func TestProjectAutomationValidationProofGatesEnablement(t *testing.T) {
	f := newAccessFixture(t)
	appRevision := bindGitHubAutomation(t, f)
	grantProject(t, f, authorization.Viewer)
	if _, err := f.store.GetProjectAutomationValidationTarget(t.Context(), f.memberSession.Token, f.project, 0); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("viewer validated automation: %v", err)
	}
	grantProject(t, f, authorization.Maintainer)
	target, err := f.store.GetProjectAutomationValidationTarget(t.Context(), f.memberSession.Token, f.project, 0)
	if err != nil || target.RepositoryExternalID != "70" || target.PipelinePath != project.DefaultPipelinePath || target.SettingsRevision != 0 {
		t.Fatalf("validation target: %#v %v", target, err)
	}
	proof := automationValidation(f.project, appRevision, 0)
	if err := f.store.RecordProjectAutomationValidation(t.Context(), f.memberSession.Token, proof); err != nil {
		t.Fatal(err)
	}
	enabled := automationUpdate(0)
	enabled.Enabled = true
	enabled.PipelinePath = project.DefaultPipelinePath
	settings, err := f.store.UpdateProjectAutomation(t.Context(), f.memberSession.Token, f.project, enabled)
	if err != nil || !settings.Enabled || settings.Revision != 1 {
		t.Fatalf("validated enable: %#v %v", settings, err)
	}
	disabled := enabled
	disabled.Enabled, disabled.ExpectedRevision = false, settings.Revision
	settings, err = f.store.UpdateProjectAutomation(t.Context(), f.memberSession.Token, f.project, disabled)
	if err != nil {
		t.Fatal(err)
	}
	enabled.ExpectedRevision = settings.Revision
	if _, err := f.store.UpdateProjectAutomation(t.Context(), f.memberSession.Token, f.project, enabled); !errors.Is(err, project.ErrAutomationNotReady) {
		t.Fatalf("stale proof enabled automation: %v", err)
	}
	proof.SettingsRevision = settings.Revision
	proof.AppRevision = uuid.New()
	if err := f.store.RecordProjectAutomationValidation(t.Context(), f.memberSession.Token, proof); !errors.Is(err, project.ErrAutomationConflict) {
		t.Fatalf("wrong App revision proof recorded: %v", err)
	}
}

func TestProjectAutomationValidationAuditFailureRollsBack(t *testing.T) {
	f := newAccessFixture(t)
	appRevision := bindGitHubAutomation(t, f)
	grantProject(t, f, authorization.Maintainer)
	rejectAudit(t, f.store)
	if err := f.store.RecordProjectAutomationValidation(t.Context(), f.memberSession.Token,
		automationValidation(f.project, appRevision, 0)); err == nil {
		t.Fatal("audit failure did not fail validation")
	}
	var rows int
	if err := f.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM repository_automation_validations WHERE repository_id=$1`, f.project).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("partial validation persisted: rows=%d error=%v", rows, err)
	}
}

func automationUpdate(revision int64) project.AutomationUpdate {
	return project.AutomationUpdate{
		PipelinePath:       "ci/pipeline.yml",
		TriggerPush:        true,
		TriggerTag:         false,
		TriggerPullRequest: true,
		CancelOlderCommits: true,
		ExpectedRevision:   revision,
	}
}

func TestProjectAutomationDefaultsAuthorizationRevisionAndAudit(t *testing.T) {
	f := newAccessFixture(t)
	grantProject(t, f, authorization.Viewer)
	settings, err := f.store.GetProjectAutomation(t.Context(), f.memberSession.Token, f.project)
	if err != nil || settings != project.DefaultAutomationSettings() {
		t.Fatalf("safe defaults: %#v %v", settings, err)
	}
	if _, err := f.store.UpdateProjectAutomation(t.Context(), f.memberSession.Token, f.project, automationUpdate(0)); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("viewer changed automation: %v", err)
	}
	grantProject(t, f, authorization.Maintainer)
	settings, err = f.store.UpdateProjectAutomation(t.Context(), f.memberSession.Token, f.project, automationUpdate(0))
	if err != nil || settings.Revision != 1 || settings.Enabled || settings.PipelinePath != "ci/pipeline.yml" || settings.TriggerTag {
		t.Fatalf("first update: %#v %v", settings, err)
	}
	read, err := f.store.GetProjectAutomation(t.Context(), f.memberSession.Token, f.project)
	if err != nil || read != settings {
		t.Fatalf("read update: %#v %v", read, err)
	}
	if _, err := f.store.UpdateProjectAutomation(t.Context(), f.memberSession.Token, f.project, automationUpdate(0)); !errors.Is(err, project.ErrAutomationConflict) {
		t.Fatalf("stale update accepted: %v", err)
	}
	next := automationUpdate(settings.Revision)
	next.TriggerTag = true
	settings, err = f.store.UpdateProjectAutomation(t.Context(), f.memberSession.Token, f.project, next)
	if err != nil || settings.Revision != 2 || !settings.TriggerTag {
		t.Fatalf("revision update: %#v %v", settings, err)
	}
	enabled := automationUpdate(settings.Revision)
	enabled.Enabled = true
	if _, err := f.store.UpdateProjectAutomation(t.Context(), f.memberSession.Token, f.project, enabled); !errors.Is(err, project.ErrAutomationNotReady) {
		t.Fatalf("unvalidated enable accepted: %v", err)
	}
	var audits int
	if err := f.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events
        WHERE actor_user_id=$1 AND action='repository.automation_updated' AND resource_id=$2
	          AND metadata->>'pipeline_path'='ci/pipeline.yml'`,
		f.member, f.project.String()).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit count=%d error=%v", audits, err)
	}
	if _, err := f.store.pool.Exec(t.Context(), `INSERT INTO repository_automation_settings
	    (repository_id,enabled,pipeline_path,trigger_push,trigger_tag,trigger_pull_request)
	    VALUES($1,false,'../pipeline.yml',true,true,true)`, f.otherProject); err == nil {
		t.Fatal("database accepted a traversal pipeline path")
	}
}

func TestProjectAutomationConcurrentFirstWriteIsCAS(t *testing.T) {
	f := newAccessFixture(t)
	grantProject(t, f, authorization.Maintainer)
	start := make(chan struct{})
	errorsOut := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			_, err := f.store.UpdateProjectAutomation(t.Context(), f.memberSession.Token, f.project, automationUpdate(0))
			errorsOut <- err
		}()
	}
	ready.Wait()
	close(start)
	succeeded, conflicted := 0, 0
	for range 2 {
		switch err := <-errorsOut; {
		case err == nil:
			succeeded++
		case errors.Is(err, project.ErrAutomationConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("CAS results: success=%d conflict=%d", succeeded, conflicted)
	}
}

func TestProjectAutomationAuditFailureRollsBack(t *testing.T) {
	f := newAccessFixture(t)
	grantProject(t, f, authorization.Maintainer)
	rejectAudit(t, f.store)
	if _, err := f.store.UpdateProjectAutomation(t.Context(), f.memberSession.Token, f.project, automationUpdate(0)); err == nil {
		t.Fatal("audit failure did not fail update")
	}
	var rows int
	if err := f.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM repository_automation_settings WHERE repository_id=$1`, f.project).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("partial settings persisted: rows=%d error=%v", rows, err)
	}
}
