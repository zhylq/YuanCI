package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/authorization"
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
