package openimages

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloaderFetchUsesVerifiedCache(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte("mock content"))
	}))
	defer server.Close()

	tempDir := t.TempDir()
	d := newTestDownloader(t, tempDir, server)
	artifact := Artifact{ID: "OI-01", URL: server.URL, Filename: "test.csv", ExpectedSHA256: sha256String("mock content"), MaxBytes: 1024}

	result, err := d.Fetch(context.Background(), artifact)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	manifest := Manifest{ToolVersion: "v1.0.0", Artifacts: []DownloadResult{result}}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(tempDir, "provenance-manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	cached, err := d.Fetch(context.Background(), artifact)
	if err != nil {
		t.Fatalf("Cached fetch failed: %v", err)
	}
	if cached.SHA256 != result.SHA256 || requests != 1 {
		t.Fatalf("Expected verified cache hit with one network request; hash=%s requests=%d", cached.SHA256, requests)
	}
}

func TestDownloaderRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("12345"))
	}))
	defer server.Close()

	tempDir := t.TempDir()
	d := newTestDownloader(t, tempDir, server)
	_, err := d.Fetch(context.Background(), Artifact{ID: "OI-01", URL: server.URL, Filename: "limit.csv", ExpectedSHA256: sha256String("12345"), MaxBytes: 3})
	if err == nil {
		t.Fatal("Expected size-limit error")
	}
	if _, statErr := os.Stat(filepath.Join(tempDir, "limit.csv.partial")); !os.IsNotExist(statErr) {
		t.Fatal("Partial file must be deleted")
	}
}

func TestDownloaderRejectsCrossHostRedirect(t *testing.T) {
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("mock content"))
	}))
	defer destination.Close()

	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := strings.Replace(destination.URL, "127.0.0.1", "localhost", 1)
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer redirect.Close()

	d := newTestDownloader(t, t.TempDir(), redirect)
	_, err := d.Fetch(context.Background(), Artifact{ID: "OI-01", URL: redirect.URL, Filename: "redirect.csv", ExpectedSHA256: sha256String("mock content"), MaxBytes: 1024})
	if err == nil {
		t.Fatal("Expected cross-host redirect rejection")
	}
}

func TestDownloaderRejectsUnapprovedInitialHostBeforeNetwork(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte("mock"))
	}))
	defer server.Close()

	d := NewDownloader(t.TempDir())
	d.client = server.Client()
	d.ApprovedHost = "approved.example"
	_, err := d.Fetch(context.Background(), Artifact{ID: "OI-01", URL: server.URL, Filename: "unapproved.csv", ExpectedSHA256: sha256String("mock"), MaxBytes: 1024})
	if err == nil {
		t.Fatal("Expected unapproved source host rejection")
	}
	if requests != 0 {
		t.Fatalf("Unapproved initial host made %d network requests", requests)
	}
}

func TestDownloaderRejectsTrustedChecksumMismatch(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("actual content"))
	}))
	defer server.Close()

	tempDir := t.TempDir()
	d := newTestDownloader(t, tempDir, server)
	_, err := d.Fetch(context.Background(), Artifact{ID: "OI-01", URL: server.URL, Filename: "checksum.csv", ExpectedSHA256: sha256String("different content"), MaxBytes: 1024})
	if err == nil {
		t.Fatal("Expected trusted checksum mismatch")
	}
	if _, statErr := os.Stat(filepath.Join(tempDir, "checksum.csv")); !os.IsNotExist(statErr) {
		t.Fatal("Checksum mismatch must not publish destination file")
	}
}

func TestDownloaderRetriesTransientFailure(t *testing.T) {
	attempts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	d := newTestDownloader(t, t.TempDir(), server)
	_, err := d.Fetch(context.Background(), Artifact{ID: "OI-01", URL: server.URL, Filename: "retry.csv", ExpectedSHA256: sha256String("unused"), MaxBytes: 1024})
	if err == nil || attempts != 3 {
		t.Fatalf("Expected three failed attempts, got attempts=%d err=%v", attempts, err)
	}
}

func newTestDownloader(t *testing.T, dir string, server *httptest.Server) *Downloader {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	d := NewDownloader(dir)
	d.client = server.Client()
	d.ApprovedHost = parsed.Hostname()
	return d
}

func sha256String(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest)
}
