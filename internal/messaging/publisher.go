package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// EventPublisher publishes events to RabbitMQ exchanges.
// *Publisher satisfies it implicitly; consumers depend on the
// abstraction so they can be tested with mocks.
type EventPublisher interface {
	Publish(exchange, routingKey string, event interface{}) error
}

// Compile-time assertion: *Publisher satisfies EventPublisher.
var _ EventPublisher = (*Publisher)(nil)

// Publisher publishes events to RabbitMQ exchanges.
type Publisher struct {
	conn *Connection
}

// NewPublisher creates a new event Publisher.
func NewPublisher(conn *Connection) *Publisher {
	return &Publisher{conn: conn}
}

// Publish publishes an event to the given exchange with the given routing key.
func (p *Publisher) Publish(exchange, routingKey string, event interface{}) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = p.conn.Channel().PublishWithContext(ctx,
		exchange,   // exchange
		routingKey, // routing key
		false,      // mandatory
		false,      // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         body,
			DeliveryMode: amqp.Persistent, // Message survives broker restart
			Timestamp:    time.Now(),
		},
	)
	if err != nil {
		return fmt.Errorf("failed to publish event: %w", err)
	}

	slog.Debug("Event published",
		"exchange", exchange,
		"routingKey", routingKey,
		"size", len(body))

	return nil
}
