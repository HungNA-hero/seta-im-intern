package main

import (
	"errors"
	"testing"
)

func TestParseReplayDeadLetterRequiresMatchingOperationalAuthorization(t *testing.T) {
	args := []string{
		"--quarantine-id", "64f4f7570b3f7b1ec67f1ea7a80ff2ec9f44acb91544a456b820087aa62ed273",
		"--job-id", "a74e1124-b5c0-47b4-b73f-4ce7c7031d77",
		"--operator-id", "10000000-0000-0000-0000-000000000001",
	}

	for name, authorization := range map[string]string{
		"missing": "",
		"wrong":   "not-the-configured-token",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseReplayDeadLetter(args, func(key string) string {
				switch key {
				case replayTokenEnvironment:
					return "configured-token"
				case replayAuthorizationEnvironment:
					return authorization
				default:
					return ""
				}
			})

			if !errors.Is(err, errReplayUnauthorized) {
				t.Fatalf("error = %v, want errReplayUnauthorized", err)
			}
		})
	}
}

func TestParseReplayDeadLetterBuildsAnAuditedRequestAfterAuthorization(t *testing.T) {
	request, err := parseReplayDeadLetter([]string{
		"--quarantine-id", "64f4f7570b3f7b1ec67f1ea7a80ff2ec9f44acb91544a456b820087aa62ed273",
		"--job-id", "a74e1124-b5c0-47b4-b73f-4ce7c7031d77",
		"--operator-id", "10000000-0000-0000-0000-000000000001",
	}, func(key string) string {
		if key == replayTokenEnvironment || key == replayAuthorizationEnvironment {
			return "configured-token"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("parseReplayDeadLetter: %v", err)
	}
	if request.QuarantineID != "64f4f7570b3f7b1ec67f1ea7a80ff2ec9f44acb91544a456b820087aa62ed273" ||
		request.JobID != "a74e1124-b5c0-47b4-b73f-4ce7c7031d77" ||
		request.Operator != "10000000-0000-0000-0000-000000000001" {
		t.Fatalf("request = %#v", request)
	}
}
