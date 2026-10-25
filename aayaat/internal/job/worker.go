package job

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/maapan/mool/gcp"
)

// CloudRunJob starts an execution of the aayaat-worker Cloud Run Job with
// args `-job <id> -tenant <tenant>` via the Cloud Run Admin API v2.
type CloudRunJob struct {
	Project string
	Region  string
	Job     string
	HTTP    *http.Client
}

// NewCloudRunJob builds the worker trigger.
func NewCloudRunJob(project, region, job string) *CloudRunJob {
	return &CloudRunJob{Project: project, Region: region, Job: job, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Trigger runs the job for one import.
func (c *CloudRunJob) Trigger(ctx context.Context, jobID, tenantID string) error {
	body, _ := json.Marshal(map[string]any{"overrides": map[string]any{
		"containerOverrides": []map[string]any{{"args": []string{"-job", jobID, "-tenant", tenantID}}},
	}})
	url := fmt.Sprintf("https://run.googleapis.com/v2/projects/%s/locations/%s/jobs/%s:run", c.Project, c.Region, c.Job)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	tok, err := gcp.AccessToken(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("cloud run job run: %d %s", res.StatusCode, b)
	}
	return nil
}
