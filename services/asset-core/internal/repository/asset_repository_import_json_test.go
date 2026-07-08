package repository

import "testing"

func TestJSONObjectsEqualPreservesLargeIntegerPrecision(t *testing.T) {
	t.Parallel()

	if jsonObjectsEqual(
		[]byte(`{"value":9007199254740992}`),
		[]byte(`{"value":9007199254740993}`),
	) {
		t.Fatal("large integers that differ by one must not compare equal")
	}

	if !jsonObjectsEqual(
		[]byte(`{"nested":{"value":9007199254740993},"name":"sample"}`),
		[]byte(`{"name":"sample","nested":{"value":9007199254740993}}`),
	) {
		t.Fatal("object key order must not affect equality")
	}
}
