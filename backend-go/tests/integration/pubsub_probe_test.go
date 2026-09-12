package integration

import (
	"context"
	"testing"
	"time"
)

func TestRawRedisPubSubRoundtrip(t *testing.T) {
	svc, rdb := testEnv(t)
	_ = svc
	if rdb == nil {
		t.Skip("needs redis")
	}
	ctx := context.Background()
	ch := "xiaoan3:probe:pubsub:test"
	sub := rdb.Subscribe(ctx, ch)
	defer sub.Close()
	msgCh := sub.Channel()
	// give the handshake a moment
	time.Sleep(200 * time.Millisecond)
	if err := rdb.Publish(ctx, ch, "hello").Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case m := <-msgCh:
		if m.Payload != "hello" {
			t.Fatalf("payload = %q", m.Payload)
		}
		t.Log("pubsub roundtrip OK")
	case <-time.After(3 * time.Second):
		t.Fatal("no message received in 3s")
	}
}
