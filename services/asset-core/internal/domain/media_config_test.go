package domain

import "testing"

func TestLoadMediaConfigDefaultsAndRefusals(t *testing.T) {
	env := map[string]string{
		"ASSET_MEDIA_REQUIRE_HTTPS":        "false",
		"ASSET_MEDIA_S3_ACCESS_KEY_ID":     "key",
		"ASSET_MEDIA_S3_SECRET_ACCESS_KEY": "secret",
	}
	lookup := func(key string) (string, bool) { value, ok := env[key]; return value, ok }

	config, err := LoadMediaConfig(lookup)
	if err != nil {
		t.Fatalf("defaults must load: %v", err)
	}
	if config.Limits.CredentialTTL.Minutes() != 60 {
		t.Fatalf("credential TTL = %v", config.Limits.CredentialTTL)
	}
	if config.Limits.SessionTTL.Hours() != 24 {
		t.Fatalf("session TTL = %v", config.Limits.SessionTTL)
	}
	if len(config.Retry.Delays) != 2 || config.Retry.Delays[0].Seconds() != 2 || config.Retry.Delays[1].Seconds() != 10 {
		t.Fatalf("retry delays = %v", config.Retry.Delays)
	}

	env["ASSET_MEDIA_REQUIRE_HTTPS"] = "true"
	if _, err := LoadMediaConfig(lookup); err == nil {
		t.Fatal("plaintext endpoint must be refused when HTTPS is required")
	}

	env["ASSET_MEDIA_REQUIRE_HTTPS"] = "false"
	env["ASSET_MEDIA_LEASE_EXPIRY_SECONDS"] = "5"
	if _, err := LoadMediaConfig(lookup); err == nil {
		t.Fatal("lease expiry below the renewal interval must be refused")
	}
}

// The processor's budgets belong to the processor, not to the reconciliation
// sweep they were originally filed under. The three must nest: a stage cannot
// be allowed to outlive the whole claimed-job deadline.
func TestLoadMediaConfigProcessorBudgets(t *testing.T) {
	env := map[string]string{
		"ASSET_MEDIA_REQUIRE_HTTPS":        "false",
		"ASSET_MEDIA_S3_ACCESS_KEY_ID":     "key",
		"ASSET_MEDIA_S3_SECRET_ACCESS_KEY": "secret",
	}
	lookup := func(key string) (string, bool) { value, ok := env[key]; return value, ok }

	config, err := LoadMediaConfig(lookup)
	if err != nil {
		t.Fatalf("defaults must load: %v", err)
	}
	if config.Processor.Validation.Seconds() != 15 {
		t.Errorf("validation budget = %v, want 15s", config.Processor.Validation)
	}
	if config.Processor.Transform.Seconds() != 60 {
		t.Errorf("transform budget = %v, want 60s", config.Processor.Transform)
	}
	if config.Processor.HardDeadline.Seconds() != 90 {
		t.Errorf("hard deadline = %v, want 90s", config.Processor.HardDeadline)
	}
	if config.Processor.ExecutablePath == "" || config.Processor.ScratchRoot == "" {
		t.Errorf("processor paths = %+v, want defaults", config.Processor)
	}

	env["ASSET_MEDIA_PROCESSOR_VALIDATION_SECONDS"] = "120"
	if _, err := LoadMediaConfig(lookup); err == nil {
		t.Fatal("a stage budget exceeding the hard deadline must be refused")
	}
}

func TestLoadMediaConfigProcessorIdentity(t *testing.T) {
	env := map[string]string{
		"ASSET_MEDIA_REQUIRE_HTTPS":        "false",
		"ASSET_MEDIA_S3_ACCESS_KEY_ID":     "key",
		"ASSET_MEDIA_S3_SECRET_ACCESS_KEY": "secret",
		"ASSET_MEDIA_PROCESSOR_UID":        "65532",
		"ASSET_MEDIA_PROCESSOR_GID":        "65532",
	}
	lookup := func(key string) (string, bool) { value, ok := env[key]; return value, ok }

	config, err := LoadMediaConfig(lookup)
	if err != nil {
		t.Fatalf("explicit processor identity must load: %v", err)
	}
	if config.Processor.UID != 65532 || config.Processor.GID != 65532 {
		t.Fatalf("processor identity = %d:%d, want 65532:65532", config.Processor.UID, config.Processor.GID)
	}

	delete(env, "ASSET_MEDIA_PROCESSOR_GID")
	if _, err := LoadMediaConfig(lookup); err == nil {
		t.Fatal("a processor UID without a GID must be refused")
	}

	delete(env, "ASSET_MEDIA_PROCESSOR_UID")
	env["ASSET_MEDIA_REQUIRE_HTTPS"] = "true"
	env["ASSET_MEDIA_S3_INTERNAL_ENDPOINT"] = "https://minio.internal"
	env["ASSET_MEDIA_S3_PUBLIC_ENDPOINT"] = "https://media.example.test"
	if _, err := LoadMediaConfig(lookup); err == nil {
		t.Fatal("non-local mode must require an isolated processor identity")
	}
}
