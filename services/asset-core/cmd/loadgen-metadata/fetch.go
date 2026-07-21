package main

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// approvedHost mirrors the host-pinning posture of internal/openimages/downloader.go.
const approvedHost = "storage.googleapis.com"

// trainArtifact is one of the Open Images V7 train-split files. These are
// too large (hundreds of MB to a few GB) to have a checksum hardcoded in the
// repo the way internal/openimages/downloader.go pins the validation split,
// so the trusted hash is resolved from GCS's own object metadata at HEAD
// time instead, then verified byte-for-byte after download.
type trainArtifact struct {
	URL      string
	Filename string
}

type permanentFetchError struct{ err error }

func (e *permanentFetchError) Error() string { return e.err.Error() }
func (e *permanentFetchError) Unwrap() error { return e.err }

func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Minute}
}

// fetchPinned downloads art.URL into dir, skipping the download if a local
// copy already matches the size+MD5 GCS currently reports for the object.
func fetchPinned(ctx context.Context, client *http.Client, art trainArtifact, dir string) (string, error) {
	destPath := filepath.Join(dir, art.Filename)

	pinnedHash, size, err := headObjectMD5(ctx, client, art.URL)
	if err != nil {
		return "", fmt.Errorf("HEAD %s: %w", art.URL, err)
	}

	if hash, ok := localFileMD5IfSizeMatches(destPath, size); ok && hash == pinnedHash {
		fmt.Printf("using cached %s (MD5 %s matches GCS)\n", art.Filename, pinnedHash)
		return destPath, nil
	}

	if err := downloadTo(ctx, client, art.URL, destPath, size); err != nil {
		return "", err
	}

	actualHash, err := md5File(destPath)
	if err != nil {
		return "", err
	}
	if actualHash != pinnedHash {
		_ = os.Remove(destPath)
		return "", fmt.Errorf("MD5 mismatch for %s: GCS reported %s, downloaded %s", art.Filename, pinnedHash, actualHash)
	}

	fmt.Printf("downloaded %s (MD5 %s verified)\n", art.Filename, actualHash)
	return destPath, nil
}

// headObjectMD5 reads the trusted MD5 GCS reports for an object via HEAD,
// decoded from the base64 x-goog-hash header into hex.
func headObjectMD5(ctx context.Context, client *http.Client, rawURL string) (hash string, size int64, err error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != approvedHost {
		return "", 0, fmt.Errorf("URL must be HTTPS on %s: %s", approvedHost, rawURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	if resp.Request.URL.Hostname() != approvedHost {
		return "", 0, fmt.Errorf("resolved unapproved host: %s", resp.Request.URL.Hostname())
	}

	size, _ = strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	if size <= 0 {
		return "", 0, fmt.Errorf("missing/invalid Content-Length")
	}

	for _, h := range resp.Header.Values("x-goog-hash") {
		parts := strings.SplitN(h, "=", 2)
		if len(parts) == 2 && parts[0] == "md5" {
			raw, err := base64.StdEncoding.DecodeString(parts[1])
			if err != nil {
				return "", 0, fmt.Errorf("decode x-goog-hash md5: %w", err)
			}
			return hex.EncodeToString(raw), size, nil
		}
	}
	return "", 0, fmt.Errorf("no md5 entry in x-goog-hash header")
}

func localFileMD5IfSizeMatches(path string, size int64) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || info.Size() != size {
		return "", false
	}
	hash, err := md5File(path)
	if err != nil {
		return "", false
	}
	return hash, true
}

func downloadTo(ctx context.Context, client *http.Client, rawURL, destPath string, expectedSize int64) error {
	partialPath := destPath + ".partial"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}

	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.Hostname() != approvedHost {
			return &permanentFetchError{err: fmt.Errorf("redirected off approved host: %s", req.URL.String())}
		}
		return nil
	}

	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	if resp.Request.URL.Hostname() != approvedHost {
		return &permanentFetchError{err: fmt.Errorf("resolved unapproved host: %s", resp.Request.URL.Hostname())}
	}

	out, err := os.Create(partialPath)
	if err != nil {
		return err
	}

	// allow a small margin over the HEAD-reported size in case of a benign mismatch
	limit := expectedSize + expectedSize/20 + 1024
	written, err := io.Copy(out, io.LimitReader(resp.Body, limit))
	closeErr := out.Close()
	if err != nil {
		_ = os.Remove(partialPath)
		return err
	}
	if closeErr != nil {
		_ = os.Remove(partialPath)
		return closeErr
	}
	if written >= limit {
		_ = os.Remove(partialPath)
		return &permanentFetchError{err: errors.New("download exceeded expected size limit")}
	}

	return os.Rename(partialPath, destPath)
}

func md5File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
