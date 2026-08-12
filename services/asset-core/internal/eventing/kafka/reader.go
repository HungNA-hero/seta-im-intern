package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"

	"seta-im-intern/go-asset-core/internal/eventing/consume"
)

type ReaderOptions struct {
	Brokers             []string
	Topic               string
	GroupID             string
	MinBytes            int
	MaxBytes            int
	MaxWait             time.Duration
	MaxDeliveryAttempts int
	RetryBackoff        time.Duration
	Security            SecurityOptions
	Logger              *slog.Logger
	OnOutcome           func(consume.Outcome)
}

// ErrDeliveryStalled ends the read loop when a record cannot be made
// committable. Offsets for it and everything after it on its partition stay
// uncommitted, so a restarted reader resumes from the last committed offset.
var ErrDeliveryStalled = errors.New("record could not be delivered within its attempt budget")

type Reader struct {
	reader   *kafka.Reader
	consumer *consume.Consumer
	options  ReaderOptions
}

func NewReader(consumer *consume.Consumer, options ReaderOptions) (*Reader, error) {
	if options.MinBytes <= 0 {
		options.MinBytes = 1
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = 1 << 20
	}
	if options.MaxWait <= 0 {
		options.MaxWait = time.Second
	}
	if options.MaxDeliveryAttempts <= 0 {
		options.MaxDeliveryAttempts = 5
	}
	if options.RetryBackoff <= 0 {
		options.RetryBackoff = 500 * time.Millisecond
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}

	dialer, err := dialerFor(options.Security)
	if err != nil {
		return nil, fmt.Errorf("configuring Kafka consumer security: %w", err)
	}

	return &Reader{
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers:     options.Brokers,
			Topic:       options.Topic,
			GroupID:     options.GroupID,
			MinBytes:    options.MinBytes,
			MaxBytes:    options.MaxBytes,
			MaxWait:     options.MaxWait,
			StartOffset: kafka.FirstOffset,
			Dialer:      dialer,
		}),
		consumer: consumer,
		options:  options,
	}, nil
}

func (reader *Reader) Run(ctx context.Context) error {
	for {
		message, err := reader.reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		}

		if err := reader.deliverUntilCommittable(ctx, message); err != nil {
			return err
		}

		if err := reader.reader.CommitMessages(ctx, message); err != nil {
			return fmt.Errorf("committing %s/%d/%d: %w", message.Topic, message.Partition, message.Offset, err)
		}
	}
}

// deliverUntilCommittable retries one record in place. A committed offset is a
// per-partition high-water mark, so skipping a record that asked to stay
// uncommitted and committing a later one would mark the skipped record consumed
// and lose it. The loop therefore never advances past an uncommittable record.
func (reader *Reader) deliverUntilCommittable(ctx context.Context, message kafka.Message) error {
	for attempt := 1; ; attempt++ {
		outcome, deliverErr := reader.consumer.Deliver(ctx, consume.Record{
			Topic:     message.Topic,
			Partition: message.Partition,
			Offset:    message.Offset,
			Key:       string(message.Key),
			Value:     message.Value,
		})
		if reader.options.OnOutcome != nil {
			reader.options.OnOutcome(outcome)
		}
		if outcome == consume.CommitOffset {
			return nil
		}

		if attempt >= reader.options.MaxDeliveryAttempts {
			reader.options.Logger.Error(
				"delivery stalled; stopping this reader with the offset uncommitted",
				"error", deliverErr,
				"attempts", attempt,
				"topic", message.Topic,
				"partition", message.Partition,
				"offset", message.Offset,
			)
			return fmt.Errorf("%w after %d attempts at %s/%d/%d: %w",
				ErrDeliveryStalled, attempt, message.Topic, message.Partition, message.Offset, deliverErr)
		}

		reader.options.Logger.Warn(
			"retrying record in place; the offset stays uncommitted",
			"error", deliverErr,
			"attempt", attempt,
			"topic", message.Topic,
			"partition", message.Partition,
			"offset", message.Offset,
		)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reader.options.RetryBackoff):
		}
	}
}

func (reader *Reader) Close() error {
	return reader.reader.Close()
}
