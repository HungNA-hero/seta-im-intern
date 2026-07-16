package requestcontext

import "testing"

func TestParseTraceparentVersioning(t *testing.T) {
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	parentID := "bbbbbbbbbbbbbbbb"

	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "version zero with four fields", value: "00-" + traceID + "-" + parentID + "-01", valid: true},
		{name: "version zero rejects extension", value: "00-" + traceID + "-" + parentID + "-01-extra", valid: false},
		{name: "future version accepts extension", value: "01-" + traceID + "-" + parentID + "-01-extra", valid: true},
		{name: "future version rejects empty extension", value: "01-" + traceID + "-" + parentID + "-01-", valid: false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := ParseTraceparent(testCase.value)
			if ok != testCase.valid {
				t.Fatalf("expected valid=%v, got valid=%v", testCase.valid, ok)
			}
			if ok && got != traceID {
				t.Fatalf("expected trace ID %q, got %q", traceID, got)
			}
		})
	}
}
