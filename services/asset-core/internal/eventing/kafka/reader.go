package kafka

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"

	"seta-im-intern/go-asset-core/internal/eventing/consume"
)

type ReaderOptions struct {
	Brokers   []string
	Topic     string
	GroupID   string
	MinBytes  int
	MaxBytes  int
	MaxWait   time.Duration
	Logger    *slog.Logger
	OnOutcome func(consume.Outcome)
}

type Reader struct {
	reader   *kafka.Reader
	consumer *consume.Consumer
	options  ReaderOptions
}

func NewReader(consumer *consume.Consumer, options ReaderOptions) *Reader {
	if options.MinBytes <= 0 {
		options.MinBytes = 1
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = 1 << 20
	}
	if options.MaxWait <= 0 {
		options.MaxWait = time.Second
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
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
		}),
		consumer: consumer,
		options:  options,
	}
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

		if outcome != consume.CommitOffset {
			reader.options.Logger.Warn(
				"leaving offset uncommitted for redelivery",
				"error", deliverErr,
				"topic", message.Topic,
				"partition", message.Partition,
				"offset", message.Offset,
			)
			continue
		}

		if err := reader.reader.CommitMessages(ctx, message); err != nil {
			reader.options.Logger.Error(
				"offset commit failed; the record will be redelivered",
				"error", err,
				"topic", message.Topic,
				"partition", message.Partition,
				"offset", message.Offset,
			)
		}
	}
}

func (reader *Reader) Close() error {
	return reader.reader.Close()
}
