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
