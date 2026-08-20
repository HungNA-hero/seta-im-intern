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

type drainingReaderEffect struct {
	started chan struct{}
	release chan struct{}
}

func (effect *drainingReaderEffect) Apply(ctx context.Context, _ event.Envelope) error {
	close(effect.started)
	select {
	case <-effect.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (effect readerEffect) Apply(context.Context, event.Envelope) error {
	return effect.err
}

type durableReaderEffect struct {
	calls       int
	activations int
}

func (effect *durableReaderEffect) Apply(context.Context, event.Envelope) error {
	effect.calls++
	if effect.activations > 0 {
		return consume.ErrAlreadyApplied
	}
	effect.activations++
	return nil
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

func TestReaderShutdownStopsNewPollsButCommitsAnEffectThatFinishesInsideTheDrainBudget(t *testing.T) {
	effect := &drainingReaderEffect{started: make(chan struct{}), release: make(chan struct{})}
	consumer := consume.NewConsumer(effect, readerQuarantine{}, consume.ConsumerOptions{
		KnownVersions:  []int{1},
		RoutableTypes:  []string{"media.processing.requested"},
		AggregateField: "jobId",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fetches, commits := 0, 0
	reader := &Reader{
		reader: &fakeReaderClient{
			fetch: func(fetchCtx context.Context) (kafkago.Message, error) {
				fetches++
				if fetches == 1 {
					return validKafkaMessage(), nil
				}
				<-fetchCtx.Done()
				return kafkago.Message{}, fetchCtx.Err()
			},
			commit: func(commitCtx context.Context, _ ...kafkago.Message) error {
				if err := commitCtx.Err(); err != nil {
					t.Fatalf("completed effect received a cancelled commit context: %v", err)
				}
				commits++
				return nil
			},
		},
		consumer: consumer,
		options: ReaderOptions{
			MaxDeliveryAttempts: 1,
			DrainTimeout:        time.Second,
			Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}

	done := make(chan error, 1)
	go func() { done <- reader.Run(ctx) }()
	<-effect.started
	cancel()
	close(effect.release)

	if err := <-done; err != nil {
		t.Fatalf("graceful Run: %v", err)
	}
	if commits != 1 {
		t.Fatalf("commits = %d, want the completed durable effect committed", commits)
	}
	if fetches != 1 {
		t.Fatalf("fetches = %d, want shutdown to prevent a new poll", fetches)
	}
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

func TestReaderOffsetCommitFailureRedeliversADurableEffectWithoutASecondActivation(t *testing.T) {
	effect := &durableReaderEffect{}
	consumer := consume.NewConsumer(effect, readerQuarantine{}, consume.ConsumerOptions{
		KnownVersions:  []int{1},
		RoutableTypes:  []string{"media.processing.requested"},
		AggregateField: "jobId",
	})
	message := validKafkaMessage()
	commitFailure := errors.New("coordinator unavailable after promotion")
	reader := &Reader{
		reader: &fakeReaderClient{
			fetch: func(context.Context) (kafkago.Message, error) { return message, nil },
			commit: func(context.Context, ...kafkago.Message) error {
				return commitFailure
			},
		},
		consumer: consumer,
		options:  validReaderOptions(),
	}

	err := reader.Run(context.Background())
	if !errors.Is(err, commitFailure) {
		t.Fatalf("Run error = %v, want offset commit failure", err)
	}
	if effect.calls != 1 || effect.activations != 1 {
		t.Fatalf("first delivery = calls %d activations %d, want one durable activation", effect.calls, effect.activations)
	}

	outcome, err := consumer.Deliver(context.Background(), consume.Record{
		Topic: message.Topic, Partition: message.Partition, Offset: message.Offset,
		Key: string(message.Key), Value: message.Value,
	})
	if err != nil || outcome != consume.CommitOffset {
		t.Fatalf("redelivery = outcome %v err %v, want committable duplicate", outcome, err)
	}
	if effect.calls != 2 || effect.activations != 1 {
		t.Fatalf("redelivery = calls %d activations %d, want one activation", effect.calls, effect.activations)
	}
}
