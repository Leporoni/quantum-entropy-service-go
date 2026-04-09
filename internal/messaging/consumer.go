package messaging

import (
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"
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
	msgs, err := c.conn.Channel().Consume(
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
func (c *Consumer) ConsumeWithPrefetch(queueName string, prefetch int, handler MessageHandler) error {
	if err := c.conn.Channel().Qos(prefetch, 0, false); err != nil {
		return err
	}
	return c.Consume(queueName, handler)
}

// SetupExchangesAndQueues declares all exchanges and queues needed by the system.
func SetupExchangesAndQueues(conn *Connection) error {
	// Declare exchanges
	exchanges := []string{
		ExchangeEntropyCollected,
		ExchangeKeyEvents,
		ExchangeAuditRequests,
		ExchangeAuditResults,
	}
	for _, ex := range exchanges {
		if err := conn.DeclareExchange(ex); err != nil {
			return err
		}
	}

	// Declare Dead Letter Exchange
	if err := conn.DeclareDeadLetterExchange("dlx.quantum"); err != nil {
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
	}

	for _, q := range queues {
		if _, err := conn.DeclareQueue(q.name, q.exchange, q.routingKey); err != nil {
			return err
		}
	}

	slog.Info("✅ All exchanges and queues declared")
	return nil
}

// Ensure amqp import is used
var _ amqp.Delivery
