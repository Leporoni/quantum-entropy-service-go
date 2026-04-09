package messaging

import (
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Connection manages the RabbitMQ connection and channel lifecycle.
type Connection struct {
	conn    *amqp.Connection
	channel *amqp.Channel
	url     string
}

// NewConnection creates and establishes a new RabbitMQ connection.
func NewConnection(url string) (*Connection, error) {
	c := &Connection{url: url}
	if err := c.connect(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Connection) connect() error {
	var err error
	// Retry logic with exponential backoff
	for i := 0; i < 5; i++ {
		c.conn, err = amqp.Dial(c.url)
		if err == nil {
			break
		}
		slog.Warn("Failed to connect to RabbitMQ, retrying...",
			"attempt", i+1, "error", err)
		time.Sleep(time.Duration(i+1) * 2 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("failed to connect to RabbitMQ after retries: %w", err)
	}

	c.channel, err = c.conn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open channel: %w", err)
	}

	slog.Info("📡 Connected to RabbitMQ", "url", c.url)
	return nil
}

// Channel returns the current AMQP channel.
func (c *Connection) Channel() *amqp.Channel {
	return c.channel
}

// Close gracefully closes the connection and channel.
func (c *Connection) Close() {
	if c.channel != nil {
		c.channel.Close()
	}
	if c.conn != nil {
		c.conn.Close()
	}
	slog.Info("RabbitMQ connection closed")
}

// DeclareExchange declares a topic exchange with the given name.
func (c *Connection) DeclareExchange(name string) error {
	return c.channel.ExchangeDeclare(
		name,    // name
		"topic", // type
		true,    // durable
		false,   // auto-deleted
		false,   // internal
		false,   // no-wait
		nil,     // arguments
	)
}

// DeclareQueue declares a durable queue and binds it to an exchange.
func (c *Connection) DeclareQueue(queueName, exchangeName, routingKey string) (amqp.Queue, error) {
	q, err := c.channel.QueueDeclare(
		queueName, // name
		true,      // durable
		false,     // delete when unused
		false,     // exclusive
		false,     // no-wait
		nil,       // arguments
	)
	if err != nil {
		return q, fmt.Errorf("failed to declare queue %s: %w", queueName, err)
	}

	err = c.channel.QueueBind(
		queueName,    // queue name
		routingKey,   // routing key
		exchangeName, // exchange
		false,        // no-wait
		nil,          // arguments
	)
	if err != nil {
		return q, fmt.Errorf("failed to bind queue %s: %w", queueName, err)
	}

	return q, nil
}

// DeclareDeadLetterExchange sets up a Dead Letter Exchange for failed messages.
func (c *Connection) DeclareDeadLetterExchange(dlxName string) error {
	return c.channel.ExchangeDeclare(
		dlxName,  // name
		"fanout", // type
		true,     // durable
		false,    // auto-deleted
		false,    // internal
		false,    // no-wait
		nil,      // arguments
	)
}
