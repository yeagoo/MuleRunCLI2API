package mulerun

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// VendorError mirrors mulerun's task_info.error object.
type VendorError struct {
	Code   int    `json:"code"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func (e *VendorError) Error() string {
	if e == nil {
		return ""
	}
	if e.Title != "" {
		return fmt.Sprintf("[%d] %s: %s", e.Code, e.Title, e.Detail)
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Detail)
}

// JobResult captures both image-style and video-style result envelopes from
// any vendor under /vendors/*/generation/{id}.
type JobResult struct {
	Status string
	Images []string
	Videos []string
	Audios []string
	Err    *VendorError
}

// taskInfo is the shared shape across all vendor endpoints.
type taskInfo struct {
	ID        string       `json:"id"`
	Status    string       `json:"status"`
	CreatedAt string       `json:"created_at"`
	UpdatedAt string       `json:"updated_at"`
	Error     *VendorError `json:"error,omitempty"`
}

type taskCreatedResponse struct {
	TaskInfo taskInfo `json:"task_info"`
}

type taskResultResponse struct {
	TaskInfo taskInfo `json:"task_info"`
	Images   []string `json:"images,omitempty"`
	Videos   []string `json:"videos,omitempty"`
	Audios   []string `json:"audios,omitempty"`
}

// Submit creates a new generation task against vendorPath (no /generation
// suffix — callers pass the full path). Returns the task ID.
func (c *Client) Submit(ctx context.Context, vendorPath string, body any) (string, error) {
	var resp taskCreatedResponse
	status, err := c.PostJSON(ctx, vendorPath, body, &resp, AuthBearer)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		// 4xx/5xx — try to surface the embedded error.
		if resp.TaskInfo.Error != nil {
			return "", resp.TaskInfo.Error
		}
		return "", fmt.Errorf("upstream HTTP %d", status)
	}
	if resp.TaskInfo.ID == "" {
		return "", fmt.Errorf("upstream returned HTTP %d without task id", status)
	}
	return resp.TaskInfo.ID, nil
}

// Poll queries the task status once. `done` is true when status is terminal
// (completed | failed). On `completed`, JobResult is populated with URLs; on
// `failed`, JobResult.Err is non-nil.
func (c *Client) Poll(ctx context.Context, vendorPath, taskID string) (JobResult, bool, error) {
	url := vendorPath + "/" + taskID
	var resp taskResultResponse
	status, err := c.GetJSON(ctx, url, &resp, AuthBearer)
	if err != nil {
		return JobResult{}, false, err
	}
	if status >= 500 {
		return JobResult{}, false, fmt.Errorf("upstream HTTP %d", status)
	}

	r := JobResult{
		Status: resp.TaskInfo.Status,
		Images: resp.Images,
		Videos: resp.Videos,
		Audios: resp.Audios,
		Err:    resp.TaskInfo.Error,
	}

	switch resp.TaskInfo.Status {
	case "completed", "succeeded":
		return r, true, nil
	case "failed":
		if r.Err == nil {
			r.Err = &VendorError{Code: status, Title: "failed", Detail: "task failed without diagnostic"}
		}
		return r, true, nil
	default:
		// pending | queued | running | processing | (unknown) — keep polling.
		return r, false, nil
	}
}

// ErrJobTimeout is returned by SubmitAndWait when the polling loop exceeds
// the configured timeout.
var ErrJobTimeout = errors.New("job did not complete before timeout")

// SubmitAndWait submits a task and polls until completion, failure, or
// timeout. Backoff: starts at initial, multiplies by 1.5 each attempt up to
// max.
func (c *Client) SubmitAndWait(ctx context.Context, vendorPath string, body any, timeout, initial, max time.Duration) (JobResult, error) {
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	id, err := c.Submit(pollCtx, vendorPath, body)
	if err != nil {
		return JobResult{}, err
	}

	wait := initial
	for {
		select {
		case <-pollCtx.Done():
			return JobResult{}, ErrJobTimeout
		case <-time.After(wait):
		}

		res, done, err := c.Poll(pollCtx, vendorPath, id)
		if err != nil {
			return JobResult{}, err
		}
		if done {
			return res, nil
		}

		wait = time.Duration(float64(wait) * 1.5)
		if wait > max {
			wait = max
		}
	}
}
