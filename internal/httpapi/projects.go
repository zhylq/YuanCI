package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/githubapp"
	"github.com/yuanci/yuanci/internal/integration"
	"github.com/yuanci/yuanci/internal/pipeline"
	"github.com/yuanci/yuanci/internal/project"
)

func projectQuery(r *http.Request, search bool) (project.Query, error) {
	q := project.Query{Limit: 20}
	// ParseQuery explicitly rejects malformed percent escapes and semicolons.
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return q, project.ErrQuery
	}
	for key, items := range values {
		if len(items) != 1 {
			return q, project.ErrQuery
		}
		switch key {
		case "limit":
			q.Limit, err = strconv.Atoi(items[0])
			if err != nil {
				return q, project.ErrQuery
			}
		case "after":
			q.After = items[0]
		case "q":
			if !search {
				return q, project.ErrQuery
			}
			q.Search = items[0]
		default:
			return q, project.ErrQuery
		}
	}
	return q, q.Validate()
}

func projectID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("projectID"))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, authorization.ErrForbidden
	}
	return id, nil
}

func automationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, project.ErrAutomationConflict):
		writeProblem(w, http.StatusConflict, "automation changed", "automation settings changed; refresh and validate again")
	case errors.Is(err, project.ErrAutomationNotReady):
		writeProblem(w, http.StatusConflict, "automation not validated", "the current GitHub App and pipeline settings must be validated before enabling automation")
	case errors.Is(err, project.ErrAutomationInvalid):
		writeProblem(w, http.StatusUnprocessableEntity, "invalid automation", "automation settings or validation identity are invalid")
	case errors.Is(err, githubapp.ErrRepositoryUnavailable), errors.Is(err, githubapp.ErrCredentialUnavailable), errors.Is(err, integration.ErrAccess):
		writeProblem(w, http.StatusConflict, "GitHub automation unavailable", "check the active GitHub App installation and repository access, then validate again")
	case errors.Is(err, integration.ErrRate):
		w.Header().Set("Retry-After", "60")
		writeProblem(w, http.StatusTooManyRequests, "GitHub rate limited", "GitHub validation is rate limited; try again later")
	default:
		var validationErrors pipeline.ValidationErrors
		var validationError pipeline.ValidationError
		if errors.As(err, &validationErrors) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"valid": false, "errors": validationErrors})
		} else if errors.As(err, &validationError) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"valid": false, "errors": []pipeline.ValidationError{validationError}})
		} else if !projectErrorHandled(w, err) {
			writeProblem(w, http.StatusBadGateway, "GitHub validation failed", "could not validate the immutable pipeline configuration")
		}
	}
}

func projectErrorHandled(w http.ResponseWriter, err error) bool {
	if errors.Is(err, authorization.ErrForbidden) {
		projectError(w, err)
		return true
	}
	return accessError(w, err)
}

func (a *API) projectAutomation(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		projectError(w, err)
		return
	}
	settings, err := a.automation.GetProjectAutomation(r.Context(), browserFrom(r).token, id)
	if err != nil {
		automationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (a *API) updateProjectAutomation(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		projectError(w, err)
		return
	}
	var update project.AutomationUpdate
	if err := decodeJSON(w, r, &update); err != nil {
		return
	}
	settings, err := a.automation.UpdateProjectAutomation(r.Context(), browserFrom(r).token, id, update)
	if err != nil {
		automationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (a *API) validateProjectAutomation(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		projectError(w, err)
		return
	}
	var request struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	target, err := a.automation.GetProjectAutomationValidationTarget(r.Context(), browserFrom(r).token, id, request.ExpectedRevision)
	if err != nil {
		automationError(w, err)
		return
	}
	if target.Provider == "gitee" {
		if a.oauth.Gitee == nil {
			automationError(w, project.ErrAutomationNotReady)
			return
		}
		proof, err := a.oauth.Gitee.ValidateProject(r.Context(), browserFrom(r).token, id, request.ExpectedRevision)
		if err != nil {
			automationError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"valid": true, "settings_revision": target.SettingsRevision, "commit_sha": proof.CommitSHA, "config_sha256": proof.ConfigSHA256, "pipeline_name": proof.PipelineName, "errors": []any{}})
		return
	}
	proof, err := a.oauth.Pipeline.ValidateDefaultPipeline(r.Context(), target.RepositoryExternalID, target.PipelinePath)
	if err != nil {
		automationError(w, err)
		return
	}
	if proof.RepositoryID != id {
		automationError(w, project.ErrAutomationConflict)
		return
	}
	validation := project.AutomationValidation{RepositoryID: proof.RepositoryID, AppRevision: proof.AppRevision,
		SettingsRevision: target.SettingsRevision, PipelinePath: target.PipelinePath, CommitSHA: proof.CommitSHA,
		ConfigSHA256: proof.ConfigSHA256, PipelineName: proof.PipelineName, ValidatedAt: time.Now().UTC()}
	if err := a.automation.RecordProjectAutomationValidation(r.Context(), browserFrom(r).token, validation); err != nil {
		automationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "settings_revision": target.SettingsRevision,
		"commit_sha": proof.CommitSHA, "config_sha256": proof.ConfigSHA256, "pipeline_name": proof.PipelineName, "errors": []any{}})
}

func projectError(w http.ResponseWriter, err error) {
	if errors.Is(err, project.ErrQuery) {
		writeProblem(w, 400, "invalid query", "分页或搜索参数无效。")
		return
	}
	if errors.Is(err, authorization.ErrForbidden) {
		writeProblem(w, 404, "project unavailable", "项目不存在或你无权访问。")
		return
	}
	if !accessError(w, err) {
		writeProblem(w, 503, "projects unavailable", "无法读取项目，请稍后重试。")
	}
}

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	q, err := projectQuery(r, true)
	if err != nil {
		projectError(w, err)
		return
	}
	page, err := a.projects.ListProjects(r.Context(), browserFrom(r).token, q)
	if err != nil {
		projectError(w, err)
		return
	}
	writeJSON(w, 200, page)
}

func (a *API) projectDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("projectID"))
	if err != nil || id == uuid.Nil {
		projectError(w, authorization.ErrForbidden)
		return
	}
	item, err := a.projects.GetProject(r.Context(), browserFrom(r).token, id)
	if err != nil {
		projectError(w, err)
		return
	}
	writeJSON(w, 200, item)
}

func (a *API) projectRuns(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("projectID"))
	if err != nil || id == uuid.Nil {
		projectError(w, authorization.ErrForbidden)
		return
	}
	q, err := projectQuery(r, false)
	if err != nil {
		projectError(w, err)
		return
	}
	page, err := a.projects.ListProjectRuns(r.Context(), browserFrom(r).token, id, q)
	if err != nil {
		projectError(w, err)
		return
	}
	writeJSON(w, 200, page)
}
