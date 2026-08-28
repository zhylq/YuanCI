// Package project defines the read-only, authorized repository browser contract.
package project

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var ErrQuery = errors.New("invalid project query")

type Query struct {
	Limit  int
	Search string
	After  string
}

func (q Query) Validate() error {
	if q.Limit < 1 || q.Limit > 100 || !utf8.ValidString(q.Search) || len(q.Search) > 100 || strings.ContainsRune(q.Search, 0) {
		return ErrQuery
	}
	return nil
}

func ProjectCursor(value string) (uuid.UUID, error) {
	if value == "" {
		return uuid.Nil, nil
	}
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value {
		return uuid.Nil, ErrQuery
	}
	return id, nil
}

// Cursors are positions, not capabilities. Every page rechecks authorization.
func RunCursor(value string) (time.Time, uuid.UUID, error) {
	if value == "" {
		return time.Time{}, uuid.Nil, nil
	}
	if len(value) > 160 {
		return time.Time{}, uuid.Nil, ErrQuery
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrQuery
	}
	parts := strings.Split(string(data), "|")
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, ErrQuery
	}
	stamp, err := time.Parse(time.RFC3339Nano, parts[0])
	id, idErr := ProjectCursor(parts[1])
	if err != nil || idErr != nil || id == uuid.Nil || stamp.IsZero() {
		return time.Time{}, uuid.Nil, ErrQuery
	}
	return stamp, id, nil
}

func EncodeRunCursor(stamp time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(stamp.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

type Organization struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}
type Record struct {
	ID               uuid.UUID    `json:"id"`
	Organization     Organization `json:"organization"`
	Provider         string       `json:"provider"`
	Owner            string       `json:"owner"`
	Name             string       `json:"name"`
	DefaultBranch    string       `json:"default_branch"`
	ConnectionStatus string       `json:"connection_status"`
}
type Run struct {
	ID           uuid.UUID  `json:"id"`
	PipelineName string     `json:"pipeline_name"`
	Event        string     `json:"event"`
	Ref          string     `json:"ref,omitempty"`
	CommitSHA    string     `json:"commit_sha,omitempty"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}
type Store interface {
	ListProjects(context.Context, string, Query) (Page[Record], error)
	GetProject(context.Context, string, uuid.UUID) (Record, error)
	ListProjectRuns(context.Context, string, uuid.UUID, Query) (Page[Run], error)
}
