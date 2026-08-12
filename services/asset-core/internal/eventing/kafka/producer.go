package kafka

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/segmentio/kafka-go"
)

type ProducerOptions struct {
	Brokers      []string
	WriteTimeout time.Duration
	MaxAttempts  int
	Security     SecurityOptions
}

type Producer struct {
	mu        sync.RWMutex
	options   ProducerOptions
	transport *kafka.Transport
	closed    bool
}

func NewProducer(options ProducerOptions) (*Producer, error) {
	if options.WriteTimeout <= 0 {
		options.WriteTimeout = 10 * time.Second
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 3
	}

	transport, err := transportFor(options.Security)
	if err != nil {
		return nil, fmt.Errorf("configuring Kafka producer security: %w", err)
	}

	return &Producer{
		options:   options,
		transport: transport,
	}, nil
}

// Publish does not return while kafka-go still owns a queued write. kafka-go's
// shared Writer may return as soon as ctx is cancelled while its partition
// goroutine continues publishing. A one-shot writer lets us close-and-join that
// goroutine before releasing the relay lease, while the contextual transport
// also cancels the background produce request at the caller's deadline.
func (producer *Producer) Publish(ctx context.Context, topic, key string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	producer.mu.RLock()
	defer producer.mu.RUnlock()
	if producer.closed {
		return io.ErrClosedPipe
	}

	writer := &kafka.Writer{
		Addr:         kafka.TCP(producer.options.Brokers...),
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireAll,
		Async:        false,
		BatchSize:    1,
		MaxAttempts:  producer.options.MaxAttempts,
		WriteTimeout: producer.options.WriteTimeout,
		Transport: &contextualRoundTripper{
			parent: ctx,
			next:   producer.transport,
		},
	}

	writeErr := writer.WriteMessages(ctx, kafka.Message{
		Topic: topic,
		Key:   []byte(key),
		Value: payload,
	})
	closeErr := writer.Close()
	return errors.Join(writeErr, closeErr)
}

func (producer *Producer) Close() error {
	producer.mu.Lock()
	defer producer.mu.Unlock()
	if producer.closed {
		return nil
	}
	producer.closed = true
	producer.transport.CloseIdleConnections()
	return nil
}

// contextualRoundTripper carries the public Publish context into kafka-go's
// background produce requests, which otherwise use context.Background().
type contextualRoundTripper struct {
	parent context.Context
	next   kafka.RoundTripper
}

func (transport *contextualRoundTripper) RoundTrip(ctx context.Context, address net.Addr, request kafka.Request) (kafka.Response, error) {
	if err := transport.parent.Err(); err != nil {
		return nil, err
	}

	requestCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(transport.parent, cancel)
	defer func() {
		stop()
		cancel()
	}()

	response, err := transport.next.RoundTrip(requestCtx, address, request)
	if err != nil && transport.parent.Err() != nil {
		return nil, transport.parent.Err()
	}
	return response, err
}
