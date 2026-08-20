package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/observability"
)

type lifecycleWorkerRepositorySpy struct {
	job          *domain.LifecycleJob
	genericCalls int
}

func (spy *lifecycleWorkerRepositorySpy) ClaimNextLifecycleJob(context.Context, string) (*domain.LifecycleJob, error) {
	return spy.job, nil
}

func (spy *lifecycleWorkerRepositorySpy) ProcessLifecycleJob(context.Context, string, string) error {
	spy.genericCalls++
	return nil
}

func (spy *lifecycleWorkerRepositorySpy) FailLifecycleJob(context.Context, string, string) error {
	return nil
}

type lifecyclePurgerSpy struct {
	calls int
}

func (spy *lifecyclePurgerSpy) Process(context.Context, string, string, time.Time) error {
	spy.calls++
	return nil
}

func TestWorkerMetricsServerExposesOnlyMetrics(t *testing.T) {
	observability.ResetMetricsForTests()
	observability.SetMetricsEnabled(true)
	t.Cleanup(observability.ResetMetricsForTests)

	server := newWorkerMetricsServer()
	recorder := httptest.NewRecorder()
	server.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/plain") {
		t.Fatalf("metrics Content-Type = %q", contentType)
	}
	if !strings.Contains(recorder.Body.String(), "seta_asset_lifecycle_event_publish_total") {
		t.Fatalf("worker metrics missing producer outcome counter:\n%s", recorder.Body.String())
	}

	notFound := httptest.NewRecorder()
	server.Handler.ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("observability-only listener exposed an unexpected route: status=%d", notFound.Code)
	}
}

func TestProcessNextRoutesPurgeToLifecyclePurger(t *testing.T) {
	leaseExpiresAt := time.Now().Add(time.Minute)
	repo := &lifecycleWorkerRepositorySpy{job: &domain.LifecycleJob{
		ID:             "purge-job",
		Operation:      domain.LifecycleJobPurge,
		LeaseExpiresAt: &leaseExpiresAt,
	}}
	purger := &lifecyclePurgerSpy{}

	processNext(context.Background(), repo, purger, "worker-a")

	if purger.calls != 1 {
		t.Fatalf("purger calls = %d, want 1 for PURGE", purger.calls)
	}
	if repo.genericCalls != 0 {
		t.Fatalf("generic lifecycle worker calls = %d, PURGE must not reach it", repo.genericCalls)
	}
}
