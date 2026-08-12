package kafka

import (
	"context"
	"net"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

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
