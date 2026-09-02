package postgres

import (
	"errors"
	"sync"
	"testing"

	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/project"
)

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
