package storage

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"

	"seta-im-intern/go-asset-core/internal/domain"
)

func TestMapObjectErrorNoSuchBucketRemainsStorageFailure(t *testing.T) {
	err := mapObjectError("head", &smithy.GenericAPIError{
		Code:    "NoSuchBucket",
		Message: "configured bucket is unavailable",
	})

	if errors.Is(err, domain.ErrObjectNotFound) {
		t.Fatalf("NoSuchBucket was mapped to a missing object: %v", err)
	}
}
