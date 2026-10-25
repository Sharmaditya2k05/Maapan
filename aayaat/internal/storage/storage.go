// Package storage keeps the original uploaded files: local disk in development,
// Google Cloud Storage on Cloud Run (JSON API + metadata-server token, no SDK).
package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/maapan/mool/gcp"
)

// Storage stores and reads back upload files by key.
type Storage interface {
	Name() string
	Put(ctx context.Context, key string, r io.Reader, contentType string) (int64, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// Local stores files under Dir (STORAGE_DIR).
type Local struct{ Dir string }

func (l Local) Name() string { return "local" }

func (l Local) path(key string) (string, error) {
	clean := filepath.Clean("/" + key)
	if strings.Contains(key, "..") {
		return "", fmt.Errorf("invalid key")
	}
	return filepath.Join(l.Dir, clean), nil
}

func (l Local) Put(_ context.Context, key string, r io.Reader, _ string) (int64, error) {
	p, err := l.path(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return 0, err
	}
	f, err := os.Create(p)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}

func (l Local) Open(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// GCS stores files in a Cloud Storage bucket (GCS_BUCKET) using the runtime
// service account's token from the metadata server.
type GCS struct {
	Bucket string
	HTTP   *http.Client
}

// NewGCS builds the GCS driver.
func NewGCS(bucket string) *GCS {
	return &GCS{Bucket: bucket, HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

func (g *GCS) Name() string { return "gcs" }

func (g *GCS) Put(ctx context.Context, key string, r io.Reader, contentType string) (int64, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	u := fmt.Sprintf("https://storage.googleapis.com/upload/storage/v1/b/%s/o?uploadType=media&name=%s",
		url.PathEscape(g.Bucket), url.QueryEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	if err := g.auth(ctx, req); err != nil {
		return 0, err
	}
	res, err := g.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return 0, fmt.Errorf("gcs upload: %d %s", res.StatusCode, b)
	}
	return int64(len(body)), nil
}

func (g *GCS) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	u := fmt.Sprintf("https://storage.googleapis.com/storage/v1/b/%s/o/%s?alt=media", url.PathEscape(g.Bucket), url.PathEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if err := g.auth(ctx, req); err != nil {
		return nil, err
	}
	res, err := g.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return nil, fmt.Errorf("gcs download: %d %s", res.StatusCode, b)
	}
	return res.Body, nil
}

func (g *GCS) auth(ctx context.Context, req *http.Request) error {
	tok, err := gcp.AccessToken(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}
