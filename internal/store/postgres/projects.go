package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/project"
)

const projectColumns = `r.id,o.id,o.display_name,r.provider,r.owner,r.name,r.default_branch,
 CASE WHEN r.github_installation_id IS NOT NULL THEN 'metadata_verified' ELSE 'not_connected' END`

func scanProject(row pgx.Row) (project.Record, error) {
	var item project.Record
	err := row.Scan(&item.ID, &item.Organization.ID, &item.Organization.Name, &item.Provider, &item.Owner, &item.Name, &item.DefaultBranch, &item.ConnectionStatus)
	return item, err
}

func (s *Store) ListProjects(ctx context.Context, token string, query project.Query) (project.Page[project.Record], error) {
	page := project.Page[project.Record]{Items: []project.Record{}}
	if err := query.Validate(); err != nil {
		return page, err
	}
	after, err := project.ProjectCursor(query.After)
	if err != nil {
		return page, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	// Membership mutations use the exclusive version of this lock. Acquire it
	// before the session/resource locks, in the same order as membership writes.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(982716421)`); err != nil {
		return page, err
	}
	session, err := authenticateSession(ctx, tx, token)
	if err != nil {
		return page, err
	}
	var instance uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM instances FOR SHARE`).Scan(&instance); err != nil {
		return page, err
	}
	rows, err := tx.Query(ctx, `SELECT role,
        CASE WHEN instance_id IS NOT NULL THEN 'instance' WHEN organization_id IS NOT NULL THEN 'organization'
             WHEN repository_id IS NOT NULL THEN 'project' ELSE 'environment' END,
        COALESCE(instance_id,organization_id,repository_id,environment_id)
        FROM memberships WHERE user_id=$1 FOR SHARE`, session.UserID)
	if err != nil {
		return page, err
	}
	grants := []authorization.Grant{}
	organizations, projects := []uuid.UUID{}, []uuid.UUID{}
	instanceRead := false
	for rows.Next() {
		g := authorization.Grant{SubjectID: session.UserID}
		if err := rows.Scan(&g.Role, &g.Scope.Kind, &g.Scope.ID); err != nil {
			rows.Close()
			return page, err
		}
		grants = append(grants, g)
		// This is a query prefilter only; the canonical policy checks each result.
		switch g.Role {
		case authorization.Viewer, authorization.Developer, authorization.Maintainer, authorization.Admin:
			switch g.Scope.Kind {
			case authorization.Instance:
				instanceRead = instanceRead || g.Scope.ID == instance
			case authorization.Organization:
				organizations = append(organizations, g.Scope.ID)
			case authorization.Project:
				projects = append(projects, g.Scope.ID)
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	for len(page.Items) <= query.Limit {
		rows, err := tx.Query(ctx, `SELECT `+projectColumns+` FROM repositories r JOIN organizations o ON o.id=r.organization_id
            WHERE r.active AND r.id>$1 AND ($2 OR r.organization_id=ANY($3::uuid[]) OR r.id=ANY($4::uuid[]))
            AND strpos(lower(r.owner || '/' || r.name),lower($5))>0
            ORDER BY r.id LIMIT 101 FOR SHARE OF r,o`, after, instanceRead, organizations, projects, query.Search)
		if err != nil {
			return page, err
		}
		count := 0
		for rows.Next() {
			item, err := scanProject(rows)
			if err != nil {
				rows.Close()
				return page, err
			}
			count++
			after = item.ID
			resource := authorization.Resource{Path: []authorization.Scope{{Kind: authorization.Instance, ID: instance}, {Kind: authorization.Organization, ID: item.Organization.ID}, {Kind: authorization.Project, ID: item.ID}}}
			if authorization.Allowed(authorization.Principal{ID: session.UserID, Active: true}, authorization.Request{Action: authorization.ResourceRead, Resource: resource}, grants) {
				page.Items = append(page.Items, item)
				if len(page.Items) > query.Limit {
					break
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return page, err
		}
		if count < 101 {
			break
		}
	}
	if len(page.Items) > query.Limit {
		page.Items = page.Items[:query.Limit]
		page.NextCursor = page.Items[len(page.Items)-1].ID.String()
	}
	if err := sessionLive(ctx, tx, session); err != nil {
		return project.Page[project.Record]{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return project.Page[project.Record]{}, err
	}
	return page, nil
}

func (s *Store) GetProject(ctx context.Context, token string, id uuid.UUID) (project.Record, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return project.Record{}, err
	}
	defer tx.Rollback(ctx)
	session, err := authenticateSession(ctx, tx, token)
	if err != nil {
		return project.Record{}, err
	}
	if err := authorize(ctx, tx, session, authorization.Scope{Kind: authorization.Project, ID: id}, authorization.ResourceRead); err != nil {
		return project.Record{}, err
	}
	item, err := scanProject(tx.QueryRow(ctx, `SELECT `+projectColumns+` FROM repositories r JOIN organizations o ON o.id=r.organization_id WHERE r.id=$1 AND r.active`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return project.Record{}, authorization.ErrForbidden
	}
	if err != nil {
		return project.Record{}, err
	}
	if err := sessionLive(ctx, tx, session); err != nil {
		return project.Record{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return project.Record{}, err
	}
	return item, nil
}

func (s *Store) ListProjectRuns(ctx context.Context, token string, id uuid.UUID, query project.Query) (project.Page[project.Run], error) {
	page := project.Page[project.Run]{Items: []project.Run{}}
	if err := query.Validate(); err != nil {
		return page, err
	}
	if query.Search != "" {
		return page, project.ErrQuery
	}
	stamp, after, err := project.RunCursor(query.After)
	if err != nil {
		return page, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	session, err := authenticateSession(ctx, tx, token)
	if err != nil {
		return page, err
	}
	if err := authorize(ctx, tx, session, authorization.Scope{Kind: authorization.Project, ID: id}, authorization.RunRead); err != nil {
		return page, err
	}
	rows, err := tx.Query(ctx, `SELECT id,pipeline_name,event,COALESCE(ref,''),COALESCE(commit_sha,''),status,created_at,started_at,finished_at
        FROM runs WHERE repository_id=$1 AND ($2::uuid='00000000-0000-0000-0000-000000000000' OR (created_at,id)<($3,$2))
        ORDER BY created_at DESC,id DESC LIMIT $4`, id, after, stamp, query.Limit+1)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var item project.Run
		if err := rows.Scan(&item.ID, &item.PipelineName, &item.Event, &item.Ref, &item.CommitSHA, &item.Status, &item.CreatedAt, &item.StartedAt, &item.FinishedAt); err != nil {
			rows.Close()
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	if len(page.Items) > query.Limit {
		page.Items = page.Items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = project.EncodeRunCursor(last.CreatedAt, last.ID)
	}
	if err := sessionLive(ctx, tx, session); err != nil {
		return project.Page[project.Run]{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return project.Page[project.Run]{}, err
	}
	return page, nil
}

var _ project.Store = (*Store)(nil)
