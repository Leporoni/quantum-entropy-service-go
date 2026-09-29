package messaging

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

var (
	// errNilSession is returned when a dial implementation hands back no session
	// and no error. Read the caveat: this guard does NOT catch a typed-nil.
	//
	// amqp.Dial cannot produce this shape (it returns either a usable connection
	// or a non-nil error), but it also never produces the case people assume it
	// does: on failure it hands back a NON-nil amqpSession interface wrapping a nil
	// *amqp.Connection, because that is how Go boxes a nil pointer. session == nil
	// therefore does not fire, and nothing here would save us. What actually saves
	// us is the order in connectLocked: err is checked before the session is looked
	// at. Keep that order. The guard stays because it is correct and cheap, and
	// because the two tests that cover it exercise a shape no production dial
	// produces — they are pinned documentation, not evidence.
	errNilSession = errors.New("dial returned no amqp session")
	// errNilChannel guards a session that opened but produced no channel. Passing
	// a nil *amqp.Channel on to declareTopology would panic inside the AMQP client.
	// Same caveat as errNilSession: the real client either returns a channel or a
	// non-nil error, so this is defensive depth against a bad amqpSession
	// implementation rather than a branch production has ever taken.
	errNilChannel = errors.New("amqp returned a nil channel")
)

// amqpSession is the subset of *amqp.Connection that Connection uses. The seam
// lets tests drive the connect, rollback and reconnect paths without a broker;
// *amqp.Connection satisfies it.
type amqpSession interface {
	Channel() (*amqp.Channel, error)
	Close() error
	IsClosed() bool
}

type dialFunc func(string) (amqpSession, error)

// Connection manages the RabbitMQ connection and channel lifecycle.
//
// The broker is dialled lazily on first use, so an outage at boot no longer
// disables messaging for the lifetime of the process.
//
// Concurrency is a plain sync.Mutex, not a sync.Once, because every step here is
// retryable: a dial can fail transiently, a topology declaration can fail
// transiently, and both the session and the channel can be replaced under us, each
// of which needs the topology declared again. A sync.Once cannot express any of
// those — it burns its single firing on the first error and poisons messaging for
// good. The rejection is spelled out in docs/PROGRESS_STATUS.md; the short version
// is that a Once spent on a failed declareTopology left the fresh channel from a
// later reconnect with no exchanges and no queues, so every Publish after a broker
// restart failed with 404 NOT_FOUND, forever.
//
// The real sync.Once in this branch lives in internal/keymanager/service.go, deriving
// the AES key: a pure function of an immutable secret, which cannot fail transiently.
type Connection struct {
	url string

	mu      sync.Mutex
	session amqpSession
	channel *amqp.Channel

	// topologyDeclared tracks whether c.channel has the exchanges/queues
	// declared. A bool under mu, not a sync.Once: a failed declaration must stay
	// retryable, and both a redial and a channel reopen hand us a fresh channel that
	// needs the topology again. Once cannot be reset and would poison messaging
	// permanently.
	topologyDeclared bool

	// lastDialFail, lastDialErr and reconnectDelay form a negative cache: a failed
	// dial is remembered for reconnectDelay so concurrent publishers do not each
	// re-dial on every event while the broker is down. lastDialErr is kept so the
	// cached error still carries the root cause for errors.Is/As.
	lastDialFail   time.Time
	lastDialErr    error
	reconnectDelay time.Duration

	// dial is a seam so tests can inject a fake; production uses amqpDial.
	dial dialFunc
	// topology is a seam over the AMQP declarations; production uses
	// declareTopology. It exists so the connected path is testable without a broker.
	topology func() error
}

// NewConnection creates a Connection without contacting the broker. The first
// Channel() call performs the dial.
func NewConnection(url string) *Connection {
	return newConnection(url, amqpDial)
}

// newConnection is the injectable constructor. The dial is a parameter rather
// than a field assignment after the fact, so a test can observe the connection
// exactly as the constructor left it.
func newConnection(url string, dial dialFunc) *Connection {
	c := &Connection{url: url, dial: dial, reconnectDelay: 5 * time.Second}
	c.topology = c.declareTopology
	return c
}

// amqpDial adapts amqp.Dial to the amqpSession seam.
func amqpDial(url string) (amqpSession, error) {
	return amqp.Dial(url)
}

// Channel returns a live channel, connecting on first use. Lazy so a broker
// that is down at boot no longer disables messaging permanently.
func (c *Connection) Channel() (*amqp.Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.connectLocked(); err != nil {
		return nil, err
	}
	if !c.topologyDeclared {
		if err := c.topology(); err != nil {
			return nil, err // not sticky: the next call retries
		}
		c.topologyDeclared = true
	}
	return c.channel, nil
}

// connectLocked makes sure a usable channel is installed. Caller must hold c.mu.
//
// Liveness is checked on the channel as well as the session. That is not
// belt-and-braces: the broker's own PRECONDITION_FAILED on a queue whose arguments
// diverge from its view kills the *channel* and leaves the TCP/AMQP session — and
// therefore every other channel on it — perfectly healthy. Checking only the
// session served that dead channel back to every later caller, which then got
// amqp.ErrClosed from Publish for the rest of the process' life: no redial, no
// re-declared topology, no error on the way out.
//
// Reopen-on-the-live-session rather than redial. A dead channel on a live session
// is by definition a session that still completed its handshake and auth, so
// redoing TCP + AMQP handshake + auth to get a second channel out of it is pure
// waste on exactly the path a broker restart storm hammers. session.Channel() is a
// single round trip. It is also not a new failure mode: if the session is dying
// underneath us, amqp091's reader goroutine marks the connection closed, so the
// next Channel() sees IsClosed() and takes the full dial path. A session that is
// alive but not answering blocks inside the reopen instead of inside the dial —
// that exposure is identical in both paths and is not what this change is about.
//
// A single attempt, not the old 5-attempt backoff: this now runs inside
// Publish, where a 30s sleep would block an HTTP request. Retry comes for
// free from the callers (the scheduler ticks every 5s, and every publish
// tries again).
func (c *Connection) connectLocked() error {
	if c.session != nil && !c.session.IsClosed() {
		if c.channel != nil && !c.channel.IsClosed() {
			return nil
		}
		return c.reopenChannelLocked()
	}
	if err := c.cachedDialError(); err != nil {
		return err
	}
	session, err := c.dial(c.url)
	if err != nil {
		c.cacheDialFailure(err)
		return fmt.Errorf("dial RabbitMQ: %w", err)
	}
	if session == nil {
		c.cacheDialFailure(errNilSession)
		return errNilSession
	}
	ch, err := session.Channel()
	if err != nil {
		_ = session.Close()
		c.cacheDialFailure(err)
		return fmt.Errorf("open channel: %w", err)
	}
	if ch == nil {
		_ = session.Close()
		c.cacheDialFailure(errNilChannel)
		return errNilChannel
	}
	// The previous session is dead but its socket and reader goroutine are not
	// released by us; close it before the fields are overwritten, otherwise every
	// broker restart leaks one connection.
	if c.session != nil {
		_ = c.session.Close()
	}
	c.session, c.channel = session, ch
	// A fresh channel has no exchanges and no queues.
	c.topologyDeclared = false
	c.lastDialFail, c.lastDialErr = time.Time{}, nil
	slog.Info("📡 Connected to RabbitMQ", "url", c.url)
	return nil
}

// reopenChannelLocked replaces a closed channel with a new one on the existing
// session. Caller must hold c.mu and must have established that the session is
// live.
func (c *Connection) reopenChannelLocked() error {
	// The channel that carried the declared topology is dead by definition, so the
	// flag goes false now, not only on success: if the reopen fails, "nothing is
	// declared" is the truth, and leaving a stale true would skip the declaration
	// on the next channel.
	c.topologyDeclared = false
	// The negative cache applies here too. A session that is up but refusing
	// channels would otherwise turn every Publish into a fresh attempt, which is
	// the same storm the cache exists to break — just over a cheaper transport.
	if err := c.cachedDialError(); err != nil {
		return err
	}
	ch, err := c.session.Channel()
	if err != nil {
		c.cacheDialFailure(err)
		return fmt.Errorf("reopen channel: %w", err)
	}
	if ch == nil {
		c.cacheDialFailure(errNilChannel)
		return errNilChannel
	}
	// amqp.Channel.Close is a no-op on an already-closed channel, so this is
	// belt-and-braces on a path where the channel is closed by definition: the
	// point is that the dead one is provably released and never resurrected.
	if c.channel != nil {
		_ = c.channel.Close()
	}
	c.channel = ch
	c.lastDialFail, c.lastDialErr = time.Time{}, nil
	slog.Info("♻️ Reopened AMQP channel on the live session", "url", c.url)
	return nil
}

// cachedDialError replays the recorded failure while the negative cache is still
// fresh, and returns nil once the window has elapsed or nothing failed. Caller must
// hold c.mu.
func (c *Connection) cachedDialError() error {
	if c.lastDialFail.IsZero() || time.Since(c.lastDialFail) >= c.reconnectDelay {
		return nil
	}
	return fmt.Errorf("RabbitMQ unavailable, last dial failed %s ago: %w",
		time.Since(c.lastDialFail).Round(time.Second), c.lastDialErr)
}

// cacheDialFailure records why the last connect attempt failed, so the negative
// cache can replay the cause instead of a bare "unavailable". Caller must hold c.mu.
func (c *Connection) cacheDialFailure(err error) {
	c.lastDialFail = time.Now()
	c.lastDialErr = err
}

// Close closes the channel and the session and returns the joined errors, if any.
//
// The AMQP close handshake is a round trip with no timeout of its own, so it
// runs after c.mu is released: holding the lock across it would block every
// Channel() — and therefore every Publish — for as long as a wedged broker
// takes to answer. The lock is taken only to steal the fields and clear them, so
// a Channel() racing this call finds a nil session and dials afresh rather than
// getting a half-closed handle.
func (c *Connection) Close() error {
	c.mu.Lock()
	session, ch := c.session, c.channel
	c.session, c.channel, c.topologyDeclared = nil, nil, false
	// The negative cache is cleared here too, for the same reason the success path
	// clears it: Close is a deliberate reset of the connection, and leaving a
	// stale failure behind means a Close/Channel() pair refuses to dial for up to
	// reconnectDelay even though nothing about the broker changed.
	c.lastDialFail, c.lastDialErr = time.Time{}, nil
	c.mu.Unlock()

	if ch == nil && session == nil {
		return nil // nothing was open: no round trip, and nothing to announce
	}

	var errs []error
	if ch != nil {
		if err := ch.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if session != nil {
		if err := session.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	slog.Info("RabbitMQ connection closed")
	return errors.Join(errs...)
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
