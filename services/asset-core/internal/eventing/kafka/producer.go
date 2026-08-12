package kafka

import (
	"context"
	"time"

	"github.com/segmentio/kafka-go"
)

type ProducerOptions struct {
	Brokers      []string
	WriteTimeout time.Duration
	MaxAttempts  int
}

type Producer struct {
	writer *kafka.Writer
}

func NewProducer(options ProducerOptions) *Producer {
	if options.WriteTimeout <= 0 {
		options.WriteTimeout = 10 * time.Second
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 3
	}

	return &Producer{writer: &kafka.Writer{
		Addr:         kafka.TCP(options.Brokers...),
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireAll,
		Async:        false,
		MaxAttempts:  options.MaxAttempts,
		WriteTimeout: options.WriteTimeout,
	}}
}

func (producer *Producer) Publish(ctx context.Context, topic, key string, payload []byte) error {
	return producer.writer.WriteMessages(ctx, kafka.Message{
		Topic: topic,
		Key:   []byte(key),
		Value: payload,
	})
}

func (producer *Producer) Close() error {
	return producer.writer.Close()
}
