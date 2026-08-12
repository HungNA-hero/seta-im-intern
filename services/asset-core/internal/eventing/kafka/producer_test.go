package kafka

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

func TestProducerRejectsAnEmptyTopicOrKeyBeforeBrokerIO(t *testing.T) {
	producer, err := NewProducer(ProducerOptions{Brokers: []string{"127.0.0.1:1"}})
	if err != nil {
		t.Fatalf("NewProducer returned error: %v", err)
	}
	defer producer.Close()

	for name, testCase := range map[string]struct {
		topic   string
		key     string
		wantErr error
	}{
		"topic": {topic: "", key: "job-1", wantErr: ErrTopicRequired},
		"key":   {topic: "media-processing.v1", key: "", wantErr: ErrKeyRequired},
	} {
		t.Run(name, func(t *testing.T) {
			err := producer.Publish(context.Background(), testCase.topic, testCase.key, []byte(`{}`))
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("Publish error = %v, want %v", err, testCase.wantErr)
			}
		})
	}
}

type blockingRoundTripper struct {
	started chan struct{}
	stopped chan struct{}
}

func (transport *blockingRoundTripper) RoundTrip(ctx context.Context, _ net.Addr, _ kafkago.Request) (kafkago.Response, error) {
	close(transport.started)
	<-ctx.Done()
	close(transport.stopped)
	return nil, ctx.Err()
}

func TestContextualRoundTripperCancelsKafkaBackgroundRequests(t *testing.T) {
	publishCtx, cancelPublish := context.WithCancel(context.Background())
	underlying := &blockingRoundTripper{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	transport := &contextualRoundTripper{parent: publishCtx, next: underlying}
	returned := make(chan error, 1)

	go func() {
		_, err := transport.RoundTrip(context.Background(), nil, nil)
		returned <- err
	}()

	select {
	case <-underlying.started:
	case <-time.After(time.Second):
		t.Fatal("underlying Kafka request did not start")
	}

	cancelPublish()

	select {
	case <-underlying.stopped:
	case <-time.After(time.Second):
		t.Fatal("Kafka background request survived cancellation of the publish lease context")
	}
	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("cancelled Kafka request returned no error")
		}
	case <-time.After(time.Second):
		t.Fatal("contextual transport did not join the cancelled request")
	}
}
