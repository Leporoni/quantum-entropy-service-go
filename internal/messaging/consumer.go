package messaging

import (
	"log/slog"
)

// MessageHandler is a function that processes a single message.
type MessageHandler func(body []byte) error

// Consumer consumes messages from a RabbitMQ queue.
type Consumer struct {
	conn *Connection
}

// NewConsumer creates a new message Consumer.
func NewConsumer(conn *Connection) *Consumer {
	return &Consumer{conn: conn}
}

// Consume starts consuming messages from the given queue in a goroutine.
// Messages are processed by the provided handler function.
// If processing fails, the message is nacked and requeued.
func (c *Consumer) Consume(queueName string, handler MessageHandler) error {
	ch, err := c.conn.Channel()
	if err != nil {
		return err
	}

	msgs, err := ch.Consume(
		queueName, // queue
		"",        // consumer tag (auto-generated)
		false,     // auto-ack (manual for reliability)
		false,     // exclusive
		false,     // no-local
		false,     // no-wait
		nil,       // args
	)
	if err != nil {
		return err
	}

	go func() {
		for msg := range msgs {
			if err := handler(msg.Body); err != nil {
				slog.Error("Failed to process message",
					"queue", queueName,
					"error", err)
				msg.Nack(false, true) // Requeue on failure
			} else {
				msg.Ack(false) // Acknowledge on success
			}
		}
	}()

	slog.Info("🎧 Consumer started", "queue", queueName)
	return nil
}

// ConsumeWithPrefetch starts consuming with a prefetch limit for backpressure control.
//
// Known wart, left alone on purpose: this calls c.conn.Channel() for the Qos call
// and Consume() calls it again for the consume call. Both are cheap after the first
// (the session is up and the topology is declared), so the duplicate is not a bug —
// but it is redundant, and collapsing it would change a public signature or invent a
// channel-accepting variant that nothing calls. PENDING: no production caller uses
// this method at all, so it is not worth an API change until there is one.
func (c *Consumer) ConsumeWithPrefetch(queueName string, prefetch int, handler MessageHandler) error {
	ch, err := c.conn.Channel()
	if err != nil {
		return err
	}
	if err := ch.Qos(prefetch, 0, false); err != nil {
		return err
	}
	return c.Consume(queueName, handler)
}

// declareTopology declares all exchanges and queues needed by the system. It is a
// method so the declaration runs on the connection's own channel, under the same
// mutex Channel() holds.
//
// It is not memoised by a sync.Once: Channel() sets Connection.topologyDeclared
// only after this returns nil, so a failure here leaves the topology undeclared
// and the next call retries. A redial or a channel reopen resets that flag, so a
// new channel gets its own declaration — which is precisely what a Once gets wrong
// (see docs/PROGRESS_STATUS.md).
//
// Coverage: 0%. Every call in here is an AMQP round trip and *amqp.Channel is a
// concrete type with no seam behind it, so a test cannot reach this function at
// all. The amqpChannel interface that would fix it is on the backlog, deliberately
// not built.
func (c *Connection) declareTopology() error {
	// Declare exchanges
	exchanges := []string{
		ExchangeEntropyCollected,
		ExchangeKeyEvents,
		ExchangeAuditRequests,
		ExchangeAuditResults,
		ExchangeEntropyPool,
	}
	for _, ex := range exchanges {
		if err := c.DeclareExchange(ex); err != nil {
			return err
		}
	}

	// Declare Dead Letter Exchange
	if err := c.DeclareDeadLetterExchange("dlx.quantum"); err != nil {
		return err
	}

	// Declare queues and bind them
	queues := []struct {
		name, exchange, routingKey string
	}{
		{"q.entropy.new", ExchangeEntropyCollected, "entropy.new"},
		{"q.entropy.validated", ExchangeEntropyCollected, "entropy.validated"},
		{"q.key.created", ExchangeKeyEvents, "key.created"},
		{"q.key.exported", ExchangeKeyEvents, "key.exported"},
		{"q.key.deleted", ExchangeKeyEvents, "key.deleted"},
		{"q.audit.start", ExchangeAuditRequests, "audit.start"},
		{"q.audit.complete", ExchangeAuditResults, "audit.complete"},
		{"q.pool.low", ExchangeEntropyPool, RoutingKeyPoolLow},
		{"q.pool.ok", ExchangeEntropyPool, RoutingKeyPoolOk},
	}

	for _, q := range queues {
		if _, err := c.DeclareQueue(q.name, q.exchange, q.routingKey); err != nil {
			return err
		}
	}

	slog.Info("✅ All exchanges and queues declared")
	return nil
}
