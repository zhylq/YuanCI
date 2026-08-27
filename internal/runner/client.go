package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	runmodel "github.com/yuanci/yuanci/internal/run"
)

type Client struct {
	baseURL *url.URL
	token   string
	client  *http.Client
}

func NewClient(rawURL, token string) (*Client, error) {
	baseURL, err := url.Parse(rawURL)
	if err != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" {
		return nil, errors.New("runner server URL must be an absolute HTTP(S) URL")
	}
	return &Client{
		baseURL: baseURL, token: token,
		client: &http.Client{Timeout: 35 * time.Second},
	}, nil
}

func (c *Client) Claim(ctx context.Context, request runmodel.ClaimRequest) (*runmodel.Assignment, error) {
	var assignment runmodel.Assignment
	status, err := c.post(ctx, "/api/v1/runner/jobs/claim", request, &assignment)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("claim job returned HTTP %d", status)
	}
	return &assignment, nil
}

func (c *Client) Start(ctx context.Context, assignment *runmodel.Assignment) error {
	status, err := c.post(ctx, "/api/v1/runner/jobs/"+assignment.JobID.String()+"/start",
		map[string]string{"lease_token": assignment.LeaseToken}, nil)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent {
		return fmt.Errorf("start job returned HTTP %d", status)
	}
	return nil
}

func (c *Client) Complete(ctx context.Context, assignment *runmodel.Assignment, status runmodel.JobStatus) error {
	responseStatus, err := c.post(ctx, "/api/v1/runner/jobs/"+assignment.JobID.String()+"/complete",
		map[string]any{"lease_token": assignment.LeaseToken, "status": status}, nil)
	if err != nil {
		return err
	}
	if responseStatus != http.StatusNoContent {
		return fmt.Errorf("complete job returned HTTP %d", responseStatus)
	}
	return nil
}

func (c *Client) post(ctx context.Context, endpoint string, payload, destination any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("encode request: %w", err)
	}
	requestURL := *c.baseURL
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + endpoint
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("runner request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		limited, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		return response.StatusCode, fmt.Errorf("runner API HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(limited)))
	}
	if destination != nil && response.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
			return response.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return response.StatusCode, nil
}
