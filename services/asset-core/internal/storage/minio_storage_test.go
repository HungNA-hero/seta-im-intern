package storage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/storage"
)

type s3Request struct {
	method  string
	path    string
	headers http.Header
	body    []byte
}

type s3Fixture struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []s3Request
	objects  map[string][]byte
}

func newS3Fixture(t *testing.T) *s3Fixture {
	t.Helper()
	fixture := &s3Fixture{objects: make(map[string][]byte)}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read S3 request body: %v", err)
		}
		fixture.mu.Lock()
		fixture.requests = append(fixture.requests, s3Request{
			method:  request.Method,
			path:    request.URL.Path,
			headers: request.Header.Clone(),
			body:    append([]byte(nil), body...),
		})
		key := strings.TrimPrefix(request.URL.Path, "/media/")
		existing, exists := fixture.objects[key]
		switch request.Method {
		case http.MethodPut:
			if exists && request.Header.Get("If-None-Match") == "*" {
				fixture.mu.Unlock()
				response.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			fixture.objects[key] = append([]byte(nil), body...)
			fixture.mu.Unlock()
			response.Header().Set("ETag", `"stored-etag"`)
			response.WriteHeader(http.StatusOK)
		case http.MethodHead:
			fixture.mu.Unlock()
			if !exists {
				response.WriteHeader(http.StatusNotFound)
				return
			}
			digest := sha256.Sum256(existing)
			response.Header().Set("Content-Length", stringInt64(int64(len(existing))))
			response.Header().Set("Content-Type", "image/png")
			response.Header().Set("X-Amz-Checksum-Sha256", base64.StdEncoding.EncodeToString(digest[:]))
			response.Header().Set("ETag", `"stored-etag"`)
			response.WriteHeader(http.StatusOK)
		case http.MethodGet:
			fixture.mu.Unlock()
			if !exists {
				response.WriteHeader(http.StatusNotFound)
				return
			}
			response.Header().Set("Content-Type", "image/png")
			_, _ = response.Write(existing)
		case http.MethodDelete:
			delete(fixture.objects, key)
			fixture.mu.Unlock()
			response.WriteHeader(http.StatusNoContent)
		default:
			fixture.mu.Unlock()
			response.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func stringInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

func newStorage(t *testing.T, internalEndpoint, publicEndpoint string) domain.ObjectStorage {
	return newStorageWithChecksumSupport(t, internalEndpoint, publicEndpoint, true)
}

func newStorageWithChecksumSupport(
	t *testing.T,
	internalEndpoint string,
	publicEndpoint string,
	checksumSupported bool,
) domain.ObjectStorage {
	t.Helper()
	adapter, err := storage.NewMinIOStorage(context.Background(), storage.MinIOConfig{
		InternalEndpoint:  internalEndpoint,
		PublicEndpoint:    publicEndpoint,
		Region:            "us-east-1",
		AccessKeyID:       "access-key",
		SecretAccessKey:   "secret-key",
		Bucket:            "media",
		ChecksumSupported: checksumSupported,
	})
	if err != nil {
		t.Fatalf("construct MinIO storage: %v", err)
	}
	return adapter
}

func TestMinIOStorage_DisabledChecksumCapabilityOmitsChecksumRequests(t *testing.T) {
	fixture := newS3Fixture(t)
	adapter := newStorageWithChecksumSupport(t, fixture.server.URL, "https://uploads.example.test", false)
	key := domain.ObjectKey("raw/org-1/asset-1/upload-1/original.png")
	digest := sha256.Sum256([]byte("image"))
	fixture.objects[key.String()] = []byte("image")

	descriptor, err := adapter.PresignPut(context.Background(), domain.PutAuthority{
		Key:               key,
		ContentType:       domain.MediaContentTypePNG,
		ExpectedSizeBytes: 5,
		ChecksumSHA256:    digest[:],
		ExpiresIn:         time.Hour,
	})
	if err != nil {
		t.Fatalf("presign PUT without provider checksum support: %v", err)
	}
	if _, exists := descriptor.Headers["X-Amz-Checksum-Sha256"]; exists {
		t.Fatalf("checksum-disabled descriptor included checksum header: %#v", descriptor.Headers)
	}
	parsed, err := url.Parse(descriptor.URL)
	if err != nil {
		t.Fatalf("parse presigned URL: %v", err)
	}
	if strings.Contains(parsed.Query().Get("X-Amz-SignedHeaders"), "x-amz-checksum-sha256") {
		t.Fatalf("checksum-disabled signature bound a checksum header: %q", parsed.Query().Get("X-Amz-SignedHeaders"))
	}

	if _, err := adapter.Head(context.Background(), key); err != nil {
		t.Fatalf("HEAD without provider checksum support: %v", err)
	}
	outputKey := domain.ObjectKey("processed/org-1/asset-1/version-1/thumbnail-256.png")
	if err := adapter.Put(context.Background(), outputKey, bytes.NewReader([]byte("image")), domain.PutAttributes{
		ContentType: domain.MediaContentTypePNG, ChecksumSHA256: digest[:],
	}); err != nil {
		t.Fatalf("PUT without provider checksum support: %v", err)
	}

	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, request := range fixture.requests {
		if request.headers.Get("X-Amz-Checksum-Mode") != "" {
			t.Fatalf("checksum-disabled request enabled checksum mode: %#v", request.headers)
		}
		if request.headers.Get("X-Amz-Checksum-Sha256") != "" {
			t.Fatalf("checksum-disabled request sent SHA-256: %#v", request.headers)
		}
	}
}

func TestMinIOStorage_PresignPutBindsOnePrivateExactKeyAndRequiredHeaders(t *testing.T) {
	fixture := newS3Fixture(t)
	adapter := newStorage(t, fixture.server.URL, "https://uploads.example.test")
	digest := sha256.Sum256([]byte("png"))
	key := domain.ObjectKey("raw/org-1/asset-1/upload-1/original.png")

	descriptor, err := adapter.PresignPut(context.Background(), domain.PutAuthority{
		Key:               key,
		ContentType:       domain.MediaContentTypePNG,
		ExpectedSizeBytes: 3,
		ChecksumSHA256:    digest[:],
		ExpiresIn:         time.Hour,
	})
	if err != nil {
		t.Fatalf("presign exact PUT: %v", err)
	}
	parsed, err := url.Parse(descriptor.URL)
	if err != nil {
		t.Fatalf("parse presigned URL: %v", err)
	}

	if descriptor.Method != http.MethodPut || parsed.Host != "uploads.example.test" {
		t.Fatalf("expected PUT signed at public host, got method=%q host=%q", descriptor.Method, parsed.Host)
	}
	if parsed.Path != "/media/"+key.String() {
		t.Fatalf("expected one exact path %q, got %q", "/media/"+key.String(), parsed.Path)
	}
	if descriptor.Headers["If-None-Match"] != "*" {
		t.Fatalf("expected conditional no-overwrite header, got %#v", descriptor.Headers)
	}
	if descriptor.Headers["Content-Type"] != "image/png" {
		t.Fatalf("expected admitted content type, got %#v", descriptor.Headers)
	}
	if descriptor.Headers["X-Amz-Checksum-Sha256"] != base64.StdEncoding.EncodeToString(digest[:]) {
		t.Fatalf("expected declared checksum header, got %#v", descriptor.Headers)
	}
	signedHeaders := parsed.Query().Get("X-Amz-SignedHeaders")
	for _, required := range []string{"content-type", "if-none-match", "x-amz-checksum-sha256"} {
		if !strings.Contains(signedHeaders, required) {
			t.Fatalf("expected %q in signed headers %q", required, signedHeaders)
		}
	}
}

func TestMinIOStorage_ServerWritesArePrivateAndNeverOverwrite(t *testing.T) {
	fixture := newS3Fixture(t)
	adapter := newStorage(t, fixture.server.URL, fixture.server.URL)
	key := domain.ObjectKey("processed/org-1/asset-1/version-1/thumbnail-256.png")
	digest := sha256.Sum256([]byte("first"))
	attributes := domain.PutAttributes{ContentType: domain.MediaContentTypePNG, ChecksumSHA256: digest[:]}

	if err := adapter.Put(context.Background(), key, bytes.NewBufferString("first"), attributes); err != nil {
		t.Fatalf("first conditional put: %v", err)
	}
	err := adapter.Put(context.Background(), key, bytes.NewBufferString("second"), attributes)
	if !errors.Is(err, domain.ErrObjectAlreadyExists) {
		t.Fatalf("expected overwrite rejection, got %v", err)
	}

	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.requests) < 2 {
		t.Fatalf("expected two PUT requests, got %d", len(fixture.requests))
	}
	for _, request := range fixture.requests[:2] {
		if request.headers.Get("If-None-Match") != "*" {
			t.Fatalf("expected unconditional no-overwrite request, got %#v", request.headers)
		}
		if request.headers.Get("X-Amz-Acl") != "" {
			t.Fatalf("private write must not request a public ACL, got %q", request.headers.Get("X-Amz-Acl"))
		}
	}
	if string(fixture.objects[key.String()]) != "first" {
		t.Fatalf("overwrite changed immutable object: %q", fixture.objects[key.String()])
	}
}

func TestMinIOStorage_HeadGetDeleteAndProviderChecksumRoundTrip(t *testing.T) {
	fixture := newS3Fixture(t)
	adapter := newStorage(t, fixture.server.URL, fixture.server.URL)
	key := domain.ObjectKey("raw/org-1/asset-1/upload-1/original.png")
	digest := sha256.Sum256([]byte("image"))
	fixture.objects[key.String()] = []byte("image")

	attributes, err := adapter.Head(context.Background(), key)
	if err != nil {
		t.Fatalf("head object: %v", err)
	}
	if attributes.SizeBytes != 5 || attributes.ContentType != "image/png" || !bytes.Equal(attributes.ChecksumSHA256, digest[:]) {
		t.Fatalf("unexpected HEAD attributes: %#v", attributes)
	}
	reader, err := adapter.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != "image" {
		t.Fatalf("unexpected GET body %q: %v", body, err)
	}
	if err := adapter.Delete(context.Background(), key); err != nil {
		t.Fatalf("delete object: %v", err)
	}
	if _, err := adapter.Head(context.Background(), key); !errors.Is(err, domain.ErrObjectNotFound) {
		t.Fatalf("expected deleted object to be absent, got %v", err)
	}
}

func TestMinIOStorage_AmbiguousWriteCanRecoverThroughExactHead(t *testing.T) {
	fixture := newS3Fixture(t)
	adapter := newStorage(t, fixture.server.URL, fixture.server.URL)
	key := domain.ObjectKey("raw/org-1/asset-1/upload-ambiguous/original.png")
	digest := sha256.Sum256([]byte("arrived"))
	fixture.objects[key.String()] = []byte("arrived")

	attributes, err := adapter.Head(context.Background(), key)
	if err != nil {
		t.Fatalf("recover ambiguous PUT with exact HEAD: %v", err)
	}
	if attributes.SizeBytes != 7 || !bytes.Equal(attributes.ChecksumSHA256, digest[:]) {
		t.Fatalf("expected stored object identity after ambiguous response, got %#v", attributes)
	}
}

func TestMinIOStorage_PresignGetRejectsRawButAllowsProcessedNamespace(t *testing.T) {
	fixture := newS3Fixture(t)
	adapter := newStorage(t, fixture.server.URL, "https://media.example.test")

	if _, err := adapter.PresignGet(context.Background(), domain.ObjectKey("raw/org/asset/upload/original.png"), 15*time.Minute); !errors.Is(err, domain.ErrRawKeyNotPresignable) {
		t.Fatalf("expected raw read authority rejection, got %v", err)
	}
	descriptor, err := adapter.PresignGet(context.Background(), domain.ObjectKey("processed/org/asset/version/web-1080.png"), 15*time.Minute)
	if err != nil {
		t.Fatalf("presign processed object: %v", err)
	}
	if !strings.HasPrefix(descriptor.URL, "https://media.example.test/media/processed/") {
		t.Fatalf("unexpected processed read URL: %q", descriptor.URL)
	}
}
