package kafka

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"seta-im-intern/go-asset-core/internal/eventing/consume"
	"seta-im-intern/go-asset-core/internal/eventing/event"
)

type readerEffect struct {
	err error
}

func (effect readerEffect) Apply(context.Context, event.Envelope) error {
	return effect.err
}

type readerQuarantine struct{}

func (readerQuarantine) Isolate(context.Context, consume.QuarantinedRecord) error {
	return nil
}

func validReaderConsumer(effectErr error) *consume.Consumer {
	return consume.NewConsumer(readerEffect{err: effectErr}, readerQuarantine{}, consume.ConsumerOptions{
		KnownVersions:  []int{1},
		RoutableTypes:  []string{"media.processing.requested"},
		AggregateField: "jobId",
	})
}

func validReaderOptions() ReaderOptions {
	return ReaderOptions{
		Brokers:             []string{"localhost:29092"},
		Topic:               "media-processing.v1",
		GroupID:             "media-workers-v1",
		MaxDeliveryAttempts: 5,
		RetryBackoff:        time.Hour,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestNewReaderReturnsConfigurationErrorsInsteadOfPanicking(t *testing.T) {
	validConsumer := validReaderConsumer(nil)
	for name, mutate := range map[string]func(*ReaderOptions) *consume.Consumer{
		"nil consumer": func(*ReaderOptions) *consume.Consumer { return nil },
		"no brokers": func(options *ReaderOptions) *consume.Consumer {
			options.Brokers = nil
			return validConsumer
		},
		"empty broker": func(options *ReaderOptions) *consume.Consumer {
			options.Brokers = []string{" "}
			return validConsumer
		},
		"empty topic": func(options *ReaderOptions) *consume.Consumer {
			options.Topic = ""
			return validConsumer
		},
		"empty group": func(options *ReaderOptions) *consume.Consumer {
			options.GroupID = ""
			return validConsumer
		},
		"invalid byte range": func(options *ReaderOptions) *consume.Consumer {
			options.MinBytes = 10
			options.MaxBytes = 5
			return validConsumer
		},
	} {
		t.Run(name, func(t *testing.T) {
			options := validReaderOptions()
			consumer := mutate(&options)
			reader, err := NewReader(consumer, options)
			if err == nil || reader != nil {
				t.Fatalf("NewReader = %v, %v; want nil reader and a configuration error", reader, err)
			}
		})
	}
}

type fakeReaderClient struct {
	fetch  func(context.Context) (kafkago.Message, error)
	commit func(context.Context, ...kafkago.Message) error
}

func (client *fakeReaderClient) FetchMessage(ctx context.Context) (kafkago.Message, error) {
	return client.fetch(ctx)
}

func (client *fakeReaderClient) CommitMessages(ctx context.Context, messages ...kafkago.Message) error {
	if client.commit == nil {
		return nil
	}
	return client.commit(ctx, messages...)
}

func (*fakeReaderClient) Close() error { return nil }

func validKafkaMessage() kafkago.Message {
	return kafkago.Message{
		Topic: "media-processing.v1",
		Key:   []byte("a74e1124-b5c0-47b4-b73f-4ce7c7031d77"),
		Value: []byte(`{"eventId":"6e2f14ea-d7c7-4f1c-8f17-274c51d9bcb9","eventType":"media.processing.requested","schemaVersion":1,"source":"asset-core","occurredAt":"2026-08-06T10:00:00Z","orgId":"10000000-0000-0000-0000-000000000001","jobId":"a74e1124-b5c0-47b4-b73f-4ce7c7031d77"}`),
	}
}

func TestReaderTreatsRunContextCancellationAsGracefulAtEveryBoundary(t *testing.T) {
	t.Run("fetch", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		reader := &Reader{
			reader: &fakeReaderClient{fetch: func(context.Context) (kafkago.Message, error) {
				return kafkago.Message{}, ctx.Err()
			}},
			consumer: validReaderConsumer(nil),
			options:  validReaderOptions(),
		}
		if err := reader.Run(ctx); err != nil {
			t.Fatalf("Run returned shutdown error: %v", err)
		}
	})

	t.Run("retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		reader := &Reader{
			reader: &fakeReaderClient{fetch: func(context.Context) (kafkago.Message, error) {
				return validKafkaMessage(), nil
			}},
			consumer: validReaderConsumer(errors.New("database unavailable")),
			options:  validReaderOptions(),
		}
		if err := reader.Run(ctx); err != nil {
			t.Fatalf("Run returned shutdown error: %v", err)
		}
	})

	t.Run("commit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		reader := &Reader{
			reader: &fakeReaderClient{
				fetch: func(context.Context) (kafkago.Message, error) { return validKafkaMessage(), nil },
				commit: func(context.Context, ...kafkago.Message) error {
					cancel()
					return context.Canceled
				},
			},
			consumer: validReaderConsumer(nil),
			options:  validReaderOptions(),
		}
		if err := reader.Run(ctx); err != nil {
			t.Fatalf("Run returned shutdown error: %v", err)
		}
	})
}

func TestReaderDoesNotHideAnUnrelatedContextShapedFetchError(t *testing.T) {
	reader := &Reader{
		reader: &fakeReaderClient{fetch: func(context.Context) (kafkago.Message, error) {
			return kafkago.Message{}, context.Canceled
		}},
		consumer: validReaderConsumer(nil),
		options:  validReaderOptions(),
	}

	err := reader.Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want the unrelated fetch cancellation propagated", err)
	}
}
