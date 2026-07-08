package openimages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Artifact defines the source and constraints for a dataset file.
type Artifact struct {
	ID             string
	URL            string
	Filename       string
	ExpectedSHA256 string
	MaxBytes       int64
}

// DownloadResult holds provenance metadata for a downloaded artifact.
type DownloadResult struct {
	SourceURL    string `json:"source_url"`
	ResolvedURL  string `json:"resolved_url"`
	Filename     string `json:"filename"`
	SHA256       string `json:"sha_256"`
	Bytes        int64  `json:"bytes"`
	DownloadedAt string `json:"downloaded_at"`
}

// Downloader manages secure, bounded downloads of Open Images artifacts.
type Downloader struct {
	client       *http.Client
	dir          string
	ApprovedHost string
}

type permanentDownloadError struct {
	err error
}

func (e *permanentDownloadError) Error() string { return e.err.Error() }
func (e *permanentDownloadError) Unwrap() error { return e.err }

// NewDownloader creates a new Downloader.
func NewDownloader(dir string) *Downloader {
	return &Downloader{
		client: &http.Client{
			Timeout: 10 * time.Minute,
		},
		dir:          dir,
		ApprovedHost: "storage.googleapis.com",
	}
}

// Fetch ensures the artifact is securely downloaded or returned from cache.
func (d *Downloader) Fetch(ctx context.Context, artifact Artifact) (DownloadResult, error) {
	parsedURL, err := url.Parse(artifact.URL)
	if err != nil || parsedURL.Scheme != "https" {
		return DownloadResult{}, fmt.Errorf("URL must be valid HTTPS: %s", artifact.URL)
	}
	if parsedURL.Hostname() != d.ApprovedHost {
		return DownloadResult{}, fmt.Errorf("source host is not approved: %s", parsedURL.Hostname())
	}
	if artifact.ExpectedSHA256 == "" {
		return DownloadResult{}, fmt.Errorf("trusted SHA256 is required for %s", artifact.Filename)
	}

	destPath := filepath.Join(d.dir, artifact.Filename)
	partialPath := destPath + ".partial"

	var manifest Manifest
	manifestPath := filepath.Join(d.dir, "provenance-manifest.json")
	manifestArtifacts := make(map[string]DownloadResult)
	if manifestData, err := os.ReadFile(manifestPath); err == nil {
		if err := json.Unmarshal(manifestData, &manifest); err == nil {
			for _, a := range manifest.Artifacts {
				manifestArtifacts[a.Filename] = a
			}
		}
	}

	info, err := os.Stat(destPath)
	if err == nil {
		if res, ok := manifestArtifacts[artifact.Filename]; ok {
			resolved, parseErr := url.Parse(res.ResolvedURL)
			if parseErr == nil && res.SourceURL == artifact.URL &&
				resolved.Scheme == "https" && resolved.Hostname() == d.ApprovedHost &&
				res.Bytes == info.Size() && info.Size() <= artifact.MaxBytes {

				hash, err := hashFile(destPath)
				if err == nil && hash == res.SHA256 && hash == artifact.ExpectedSHA256 {
					return res, nil
				}
			}
		}
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		res, err := d.tryDownload(ctx, artifact, destPath, partialPath)
		if err == nil {
			return res, nil
		}
		lastErr = err
		os.Remove(partialPath)
		var permanent *permanentDownloadError
		if errors.As(err, &permanent) {
			return DownloadResult{}, permanent
		}

		select {
		case <-ctx.Done():
			return DownloadResult{}, ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
	return DownloadResult{}, fmt.Errorf("failed after 3 attempts: %w", lastErr)
}

func (d *Downloader) tryDownload(ctx context.Context, artifact Artifact, destPath, partialPath string) (DownloadResult, error) {
	client := *d.client // shallow copy to preserve transport
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !strings.HasPrefix(req.URL.Scheme, "https") {
			return &permanentDownloadError{err: fmt.Errorf("redirected to non-HTTPS URL: %s", req.URL.String())}
		}
		if req.URL.Hostname() != d.ApprovedHost {
			return &permanentDownloadError{err: fmt.Errorf("redirected to unapproved host: %s", req.URL.Hostname())}
		}
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return DownloadResult{}, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return DownloadResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return DownloadResult{}, fmt.Errorf("unexpected HTTP status: %d", resp.StatusCode)
	}

	if resp.Request.URL.Hostname() != d.ApprovedHost {
		return DownloadResult{}, &permanentDownloadError{err: fmt.Errorf("resolved unapproved host: %s", resp.Request.URL.Hostname())}
	}

	out, err := os.Create(partialPath)
	if err != nil {
		return DownloadResult{}, err
	}

	hasher := sha256.New()
	writer := io.MultiWriter(out, hasher)

	limitReader := io.LimitReader(resp.Body, artifact.MaxBytes)
	written, err := io.Copy(writer, limitReader)
	if err != nil {
		out.Close()
		return DownloadResult{}, err
	}

	extra := make([]byte, 1)
	n, _ := resp.Body.Read(extra)
	if n > 0 {
		out.Close()
		return DownloadResult{}, &permanentDownloadError{err: fmt.Errorf("file exceeds maximum size of %d bytes", artifact.MaxBytes)}
	}

	if err := out.Close(); err != nil {
		return DownloadResult{}, err
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	if digest != artifact.ExpectedSHA256 {
		return DownloadResult{}, &permanentDownloadError{err: fmt.Errorf("trusted SHA256 mismatch for %s: expected %s, got %s", artifact.Filename, artifact.ExpectedSHA256, digest)}
	}

	if err := os.Rename(partialPath, destPath); err != nil {
		return DownloadResult{}, err
	}

	return DownloadResult{
		SourceURL:    artifact.URL,
		ResolvedURL:  resp.Request.URL.String(),
		Filename:     artifact.Filename,
		SHA256:       digest,
		Bytes:        written,
		DownloadedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
