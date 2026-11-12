package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBrokerSend(t *testing.T) {
	broker := NewBroker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go broker.Run(ctx)

	// Register a client
	ch := make(chan SSEEvent, 32)
	broker.register <- ch

	// Give the broker time to register
	time.Sleep(10 * time.Millisecond)

	broker.Send(SSEEvent{Event: "test", Data: `{"msg":"hello"}`})

	select {
	case evt := <-ch:
		if evt.Event != "test" {
			t.Errorf("event = %q, want %q", evt.Event, "test")
		}
		if evt.Data != `{"msg":"hello"}` {
			t.Errorf("data = %q, want %q", evt.Data, `{"msg":"hello"}`)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for SSE event")
	}

	// Unregister
	broker.unregister <- ch
	time.Sleep(10 * time.Millisecond)
}

func TestBrokerContextCancellation(t *testing.T) {
	broker := NewBroker()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		broker.Run(ctx)
		close(done)
	}()

	ch := make(chan SSEEvent, 32)
	broker.register <- ch
	time.Sleep(10 * time.Millisecond)

	cancel()

	select {
	case <-done:
		// OK - broker exited
	case <-time.After(time.Second):
		t.Fatal("broker did not exit after context cancellation")
	}
}

func TestBrokerBroadcastToMultipleClients(t *testing.T) {
	broker := NewBroker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go broker.Run(ctx)

	ch1 := make(chan SSEEvent, 32)
	ch2 := make(chan SSEEvent, 32)
	broker.register <- ch1
	broker.register <- ch2
	time.Sleep(10 * time.Millisecond)

	broker.Send(SSEEvent{Event: "update", Data: "data"})

	for _, ch := range []chan SSEEvent{ch1, ch2} {
		select {
		case evt := <-ch:
			if evt.Event != "update" {
				t.Errorf("event = %q, want %q", evt.Event, "update")
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for event")
		}
	}
}

func TestServeHTTPSendsHeadersBeforeFirstEvent(t *testing.T) {
	broker := NewBroker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go broker.Run(ctx)

	srv := httptest.NewServer(broker)
	defer srv.Close()

	// No event is ever sent: the response headers alone must arrive promptly.
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("headers did not arrive before any event: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}

func TestBrokerClients(t *testing.T) {
	broker := NewBroker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go broker.Run(ctx)

	if n := broker.Clients(); n != 0 {
		t.Fatalf("clients = %d, want 0", n)
	}
	ch := make(chan SSEEvent, 1)
	broker.register <- ch
	broker.Send(SSEEvent{Event: "sync"}) // processed after the registration
	<-ch
	if n := broker.Clients(); n != 1 {
		t.Errorf("clients = %d, want 1", n)
	}
	if n := broker.Connects(); n != 1 {
		t.Errorf("connects = %d, want 1", n)
	}
	broker.unregister <- ch
	broker.Send(SSEEvent{Event: "sync"})
	time.Sleep(10 * time.Millisecond)
	if n := broker.Clients(); n != 0 {
		t.Errorf("clients after unregister = %d, want 0", n)
	}
}
