package storage

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"

	"seta-im-intern/go-asset-core/internal/domain"
)

// MinIOConfig keeps the private service endpoint distinct from the externally
// reachable endpoint used in signatures. A signed URL is never rewritten.
type MinIOConfig struct {
	InternalEndpoint  string
	PublicEndpoint    string
	Region            string
	AccessKeyID       string
	SecretAccessKey   string
	Bucket            string
	ChecksumSupported bool
}

type MinIOStorage struct {
	bucket            string
	publicEndpoint    string
	region            string
	credentials       aws.Credentials
	signer            *awsv4.Signer
	internal          *s3.Client
	publicPresigner   *s3.PresignClient
	checksumSupported bool
}

func NewMinIOStorage(ctx context.Context, config MinIOConfig) (*MinIOStorage, error) {
	if err := validateMinIOConfig(config); err != nil {
		return nil, err
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(config.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			config.AccessKeyID,
			config.SecretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("load S3 configuration: %w", err)
	}
	newClient := func(endpoint string) *s3.Client {
		return s3.NewFromConfig(awsConfig, func(options *s3.Options) {
			options.BaseEndpoint = aws.String(endpoint)
			options.UsePathStyle = true
		})
	}
	return &MinIOStorage{
		bucket:         config.Bucket,
		publicEndpoint: strings.TrimRight(config.PublicEndpoint, "/"),
		region:         config.Region,
		credentials: aws.Credentials{
			AccessKeyID: config.AccessKeyID, SecretAccessKey: config.SecretAccessKey,
		},
		signer:            awsv4.NewSigner(),
		internal:          newClient(config.InternalEndpoint),
		publicPresigner:   s3.NewPresignClient(newClient(config.PublicEndpoint)),
		checksumSupported: config.ChecksumSupported,
	}, nil
}

func validateMinIOConfig(config MinIOConfig) error {
	for name, value := range map[string]string{
		"internal endpoint": config.InternalEndpoint,
		"public endpoint":   config.PublicEndpoint,
		"region":            config.Region,
		"access key":        config.AccessKeyID,
		"secret key":        config.SecretAccessKey,
		"bucket":            config.Bucket,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("MinIO %s is required", name)
		}
	}
	for _, rawEndpoint := range []string{config.InternalEndpoint, config.PublicEndpoint} {
		parsed, err := url.Parse(rawEndpoint)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("invalid MinIO endpoint %q", rawEndpoint)
		}
	}
	return nil
}

func (storage *MinIOStorage) PresignPut(ctx context.Context, authority domain.PutAuthority) (domain.UploadDescriptor, error) {
	if authority.Key == "" {
		return domain.UploadDescriptor{}, errors.New("presigned PUT key is required")
	}
	// SigV4 expresses expiry in whole seconds, so a sub-second grant would sign
	// X-Amz-Expires=0 and hand back a URL that is rejected on first use.
	expiresInSeconds := int64(authority.ExpiresIn / time.Second)
	if expiresInSeconds < 1 {
		return domain.UploadDescriptor{}, errors.New("presigned PUT expiry must be at least one second")
	}
	if authority.ExpectedSizeBytes < domain.MinUploadSizeBytes || authority.ExpectedSizeBytes > domain.MaxUploadSizeBytes {
		return domain.UploadDescriptor{}, fmt.Errorf("presigned PUT size %d is outside admission bounds", authority.ExpectedSizeBytes)
	}
	if len(authority.ChecksumSHA256) != domain.ChecksumByteLength {
		return domain.UploadDescriptor{}, errors.New("presigned PUT requires a 32-byte SHA-256")
	}
	checksum := base64.StdEncoding.EncodeToString(authority.ChecksumSHA256)
	contentType := string(authority.ContentType)
	if _, ok := authority.ContentType.FileExtension(); !ok {
		return domain.UploadDescriptor{}, domain.ErrUnsupportedMediaType
	}
	objectURL := storage.publicEndpoint + "/" + url.PathEscape(storage.bucket) + "/" + escapeObjectKey(authority.Key.String())
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, objectURL, nil)
	if err != nil {
		return domain.UploadDescriptor{}, fmt.Errorf("build presigned PUT: %w", err)
	}
	request.ContentLength = authority.ExpectedSizeBytes
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("If-None-Match", "*")
	if storage.checksumSupported {
		request.Header.Set("X-Amz-Checksum-Sha256", checksum)
	}
	query := request.URL.Query()
	query.Set("X-Amz-Expires", strconv.FormatInt(expiresInSeconds, 10))
	request.URL.RawQuery = query.Encode()
	signedAt := time.Now().UTC()
	signedURL, signedHeaders, err := storage.signer.PresignHTTP(
		ctx,
		storage.credentials,
		request,
		"UNSIGNED-PAYLOAD",
		"s3",
		storage.region,
		signedAt,
		func(options *awsv4.SignerOptions) { options.DisableHeaderHoisting = true },
	)
	if err != nil {
		return domain.UploadDescriptor{}, fmt.Errorf("presign PUT: %w", err)
	}
	descriptorHeaders := make(map[string]string, len(signedHeaders))
	for name, values := range signedHeaders {
		if strings.EqualFold(name, "Host") || len(values) == 0 {
			continue
		}
		descriptorHeaders[name] = values[0]
	}
	return domain.UploadDescriptor{
		Protocol:            "HTTP",
		Method:              http.MethodPut,
		URL:                 signedURL,
		Headers:             descriptorHeaders,
		CredentialExpiresAt: signedAt.Add(time.Duration(expiresInSeconds) * time.Second),
	}, nil
}

func escapeObjectKey(key string) string {
	parts := strings.Split(key, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}

func (storage *MinIOStorage) PresignGet(ctx context.Context, key domain.ObjectKey, ttl time.Duration) (domain.DownloadDescriptor, error) {
	if key.IsRaw() {
		return domain.DownloadDescriptor{}, domain.ErrRawKeyNotPresignable
	}
	if !strings.HasPrefix(key.String(), "processed/") {
		return domain.DownloadDescriptor{}, errors.New("only processed object keys can be signed for read")
	}
	if ttl <= 0 {
		return domain.DownloadDescriptor{}, errors.New("presigned GET expiry must be positive")
	}
	result, err := storage.publicPresigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(storage.bucket),
		Key:    aws.String(key.String()),
	}, func(options *s3.PresignOptions) {
		options.Expires = ttl
	})
	if err != nil {
		return domain.DownloadDescriptor{}, fmt.Errorf("presign GET: %w", err)
	}
	return domain.DownloadDescriptor{URL: result.URL, ExpiresAt: time.Now().UTC().Add(ttl)}, nil
}

func (storage *MinIOStorage) Head(ctx context.Context, key domain.ObjectKey) (domain.ObjectAttributes, error) {
	input := &s3.HeadObjectInput{
		Bucket: aws.String(storage.bucket),
		Key:    aws.String(key.String()),
	}
	if storage.checksumSupported {
		input.ChecksumMode = types.ChecksumModeEnabled
	}
	result, err := storage.internal.HeadObject(ctx, input)
	if err != nil {
		return domain.ObjectAttributes{}, mapObjectError("head", err)
	}
	var checksum []byte
	if result.ChecksumSHA256 != nil && *result.ChecksumSHA256 != "" {
		checksum, err = base64.StdEncoding.DecodeString(*result.ChecksumSHA256)
		if err != nil {
			return domain.ObjectAttributes{}, fmt.Errorf("decode storage SHA-256: %w", err)
		}
	}
	return domain.ObjectAttributes{
		SizeBytes:      aws.ToInt64(result.ContentLength),
		ContentType:    aws.ToString(result.ContentType),
		ChecksumSHA256: checksum,
		ETag:           strings.Trim(aws.ToString(result.ETag), `"`),
	}, nil
}

func (storage *MinIOStorage) Put(ctx context.Context, key domain.ObjectKey, body io.Reader, attributes domain.PutAttributes) error {
	if len(attributes.ChecksumSHA256) != domain.ChecksumByteLength {
		return errors.New("server-side PUT requires a 32-byte SHA-256")
	}
	if _, ok := attributes.ContentType.FileExtension(); !ok {
		return domain.ErrUnsupportedMediaType
	}
	input := &s3.PutObjectInput{
		Bucket:      aws.String(storage.bucket),
		Key:         aws.String(key.String()),
		Body:        body,
		ContentType: aws.String(string(attributes.ContentType)),
		IfNoneMatch: aws.String("*"),
	}
	if storage.checksumSupported {
		input.ChecksumSHA256 = aws.String(base64.StdEncoding.EncodeToString(attributes.ChecksumSHA256))
	}
	_, err := storage.internal.PutObject(ctx, input, func(options *s3.Options) {
		options.APIOptions = append(options.APIOptions, useUnsignedPayload)
	})
	if err != nil {
		return mapObjectError("put", err)
	}
	return nil
}

func useUnsignedPayload(stack *middleware.Stack) error {
	awsv4.RemoveComputePayloadSHA256Middleware(stack)
	return awsv4.AddUnsignedPayloadMiddleware(stack)
}

func (storage *MinIOStorage) Get(ctx context.Context, key domain.ObjectKey) (io.ReadCloser, error) {
	result, err := storage.internal.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(storage.bucket),
		Key:    aws.String(key.String()),
	})
	if err != nil {
		return nil, mapObjectError("get", err)
	}
	return result.Body, nil
}

func (storage *MinIOStorage) Delete(ctx context.Context, key domain.ObjectKey) error {
	_, err := storage.internal.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(storage.bucket),
		Key:    aws.String(key.String()),
	})
	if err != nil {
		return mapObjectError("delete", err)
	}
	return nil
}

func mapObjectError(operation string, err error) error {
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return fmt.Errorf("%s object: %w", operation, domain.ErrObjectNotFound)
		case "PreconditionFailed", "ConditionalRequestConflict":
			return fmt.Errorf("%s object: %w", operation, domain.ErrObjectAlreadyExists)
		}
	}
	return fmt.Errorf("%s object storage request: %w", operation, err)
}

var _ domain.ObjectStorage = (*MinIOStorage)(nil)
