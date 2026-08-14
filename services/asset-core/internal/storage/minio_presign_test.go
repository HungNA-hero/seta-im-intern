package storage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
)

func TestMinIOPresign_WholeObjectAuthorityIsBoundToExactPublicKeyAndHeaders(t *testing.T) {
	adapter := newStorage(t, "http://minio.internal.test:9000", "https://uploads.public.test")
	body := []byte("complete file")
	digest := sha256.Sum256(body)
	key := domain.ObjectKey("raw/org-1/asset-1/upload-1/original.png")

	issuedAt := time.Now().UTC()
	descriptor, err := adapter.PresignPut(context.Background(), domain.PutAuthority{
		Key:               key,
		ContentType:       domain.MediaContentTypePNG,
		ExpectedSizeBytes: int64(len(body)),
		ChecksumSHA256:    digest[:],
		ExpiresIn:         time.Hour,
	})
	if err != nil {
		t.Fatalf("presign whole-object PUT: %v", err)
	}
	parsed, err := url.Parse(descriptor.URL)
	if err != nil {
		t.Fatalf("parse signed URL: %v", err)
	}

	if descriptor.Protocol != "HTTP" || descriptor.Method != http.MethodPut {
		t.Fatalf("descriptor = %s %s, want HTTP PUT", descriptor.Protocol, descriptor.Method)
	}
	if parsed.Scheme != "https" || parsed.Host != "uploads.public.test" {
		t.Fatalf("signed public origin = %s://%s", parsed.Scheme, parsed.Host)
	}
	if parsed.Path != "/media/"+key.String() {
		t.Fatalf("signed key path = %q, want exact key %q", parsed.Path, "/media/"+key.String())
	}
	if parsed.Query().Get("X-Amz-Expires") != "3600" {
		t.Fatalf("credential lifetime = %q, want 3600 seconds", parsed.Query().Get("X-Amz-Expires"))
	}
	if descriptor.CredentialExpiresAt.Before(issuedAt.Add(time.Hour-time.Second)) ||
		descriptor.CredentialExpiresAt.After(time.Now().UTC().Add(time.Hour+time.Second)) {
		t.Fatalf("credential expiry = %s, want issuance plus 60 minutes", descriptor.CredentialExpiresAt)
	}

	wantChecksum := base64.StdEncoding.EncodeToString(digest[:])
	for name, want := range map[string]string{
		"Content-Type":          "image/png",
		"Content-Length":        "13",
		"If-None-Match":         "*",
		"X-Amz-Checksum-Sha256": wantChecksum,
	} {
		if got := descriptor.Headers[name]; got != want {
			t.Errorf("descriptor header %s = %q, want %q (all headers: %#v)", name, got, want, descriptor.Headers)
		}
	}
	signedHeaders := parsed.Query().Get("X-Amz-SignedHeaders")
	for _, name := range []string{"content-length", "content-type", "if-none-match", "x-amz-checksum-sha256"} {
		if !strings.Contains(signedHeaders, name) {
			t.Errorf("signed headers %q do not bind %q", signedHeaders, name)
		}
	}
}

func TestMinIOPresign_LostPutResponseRecoversOnlyThroughExactObjectHead(t *testing.T) {
	fixture := newS3Fixture(t)
	adapter := newStorage(t, fixture.server.URL, "https://uploads.public.test")
	exactKey := domain.ObjectKey("raw/org-1/asset-1/upload-lost/original.png")
	body := []byte("complete retry payload")
	digest := sha256.Sum256(body)
	fixture.objects[exactKey.String()] = append([]byte(nil), body...)

	attributes, err := adapter.Head(context.Background(), exactKey)
	if err != nil {
		t.Fatalf("HEAD exact object after lost PUT response: %v", err)
	}
	if attributes.SizeBytes != int64(len(body)) || attributes.ContentType != "image/png" ||
		!bytes.Equal(attributes.ChecksumSHA256, digest[:]) {
		t.Fatalf("recovered identity = %#v", attributes)
	}

	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.requests) != 1 || fixture.requests[0].method != http.MethodHead ||
		fixture.requests[0].path != "/media/"+exactKey.String() {
		t.Fatalf("recovery requests = %#v, want one exact-key HEAD", fixture.requests)
	}
}
