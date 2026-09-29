package messaging

import (
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// errRefused is the root cause the failing-dial tests inject. They assert on it
// with errors.Is, which is how the negative cache is proved to keep it.
var errRefused = errors.New("connection refused")

// --- fakes ---

// dialRecorder is a fake for the Connection.dial seam. It never touches the
// network: every call fails, so these tests need no broker and no RabbitMQ. It
// records the URL it was handed, because "the dial received the URL the
// connection was built with" is a contract of its own.
type dialRecorder struct {
	calls atomic.Int64
	err   error
	urls  []string
}

func (d *dialRecorder) dial(url string) (amqpSession, error) {
	d.calls.Add(1)
	d.urls = append(d.urls, url)
	return nil, d.err
}

// fakeSession is a hand-written amqpSession. amqp091-go exports no constructor
// for a *amqp.Connection other than Dial/DialConfig, so this is the only way to
// reach the connect, rollback and reconnect branches without a broker.
//
// A zero &amqp.Channel{} is a usable stand-in for the live channel: IsClosed()
// reads one int32 field and is safe on it. Its Close() is NOT safe — it
// dereferences a nil ch.connection and panics — so a test that ends up holding
// one must not close the Connection, unless the channel was marked closed first
// (see markChannelClosed, after which Close is a documented no-op).
type fakeSession struct {
	mu         sync.Mutex
	ch         *amqp.Channel
	chErr      error
	closed     bool
	closeCalls int
	// makeCh, when set, is called on every Channel() invocation and its result is
	// returned instead of ch. A test that needs to tell a reopened channel from the
	// one it replaced hands out a fresh one per call; returning nil exercises the
	// nil-channel guard. ch is used when makeCh is nil.
	makeCh func() *amqp.Channel
}

func (f *fakeSession) Channel() (*amqp.Channel, error) {
	if f.chErr != nil {
		return nil, f.chErr
	}
	if f.makeCh != nil {
		return f.makeCh(), nil
	}
	return f.ch, nil
}

func (f *fakeSession) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.closeCalls++
	return nil
}

func (f *fakeSession) IsClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeSession) closes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCalls
}

// deadChannelSession returns a session that reports itself alive and hands out a
// channel that is already closed. It exists for the tests that call Connection.Close()
// concurrently with Channel(): the dead channel makes ch.Close() a no-op instead of a
// nil dereference, and a live session keeps the reconnect decision on the reopen path
// rather than the dial path.
func deadChannelSession() *fakeSession {
	return &fakeSession{makeCh: func() *amqp.Channel {
		ch := &amqp.Channel{}
		markChannelClosed(ch)
		return ch
	}}
}

// markChannelClosed flips amqp091-go's unexported closed flag, which is what the
// broker itself does when it kills a channel with a channel-level exception such as
// PRECONDITION_FAILED. There is no exported way to do that, and Connection must be
// able to see it, so the flag is written through reflection.
//
// reflect.Value.UnsafePointer is used instead of UnsafeAddr because go vet rejects
// the uintptr round trip. The field is an int32 that the client reads with
// sync/atomic, so it is written the same way.
func markChannelClosed(ch *amqp.Channel) {
	field := reflect.ValueOf(ch).Elem().FieldByName("closed")
	atomic.StoreInt32((*int32)(field.Addr().UnsafePointer()), 1)
}

// quietLogs swaps the default logger for a discarding one for the duration of the
// test. The tests that drive a connected Connection log one line per connect, and
// under -count=50 that is thousands of lines of noise in an otherwise green run.
func quietLogs(t *testing.T) {
	t.Helper()
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
}

// --- helpers ---

// newTestConnection builds a Connection whose dial always fails, so no test
// needs a broker. reconnectDelay is applied here, before the Connection is
// visible to any goroutine, so the mutation needs no lock.
func newTestConnection(t *testing.T, reconnectDelay time.Duration) (*Connection, *dialRecorder) {
	t.Helper()
	rec := &dialRecorder{err: errRefused}
	c := newConnection("amqp://guest:guest@rabbitmq:5672/", rec.dial)
	c.reconnectDelay = reconnectDelay
	t.Cleanup(func() { _ = c.Close() }) // safe: the dial never succeeded, so no channel is held
	return c, rec
}

// staticDial is the one-session-every-time case of newConnectedTestConnection.
func staticDial(s amqpSession) dialFunc {
	return func(string) (amqpSession, error) { return s, nil }
}

// dialSequence hands out the given sessions in order, one per call, and keeps
// returning the last one after that. It exists so a reconnect test can tell the
// replacement session from the one it replaced: a dial that always returns the
// same session satisfies every "the session was replaced" assertion with the
// original object.
func dialSequence(sessions ...amqpSession) (dialFunc, *atomic.Int64) {
	if len(sessions) == 0 {
		panic("dialSequence needs at least one session")
	}
	var calls atomic.Int64
	return func(string) (amqpSession, error) {
		i := int(calls.Add(1)) - 1
		if i >= len(sessions) {
			i = len(sessions) - 1
		}
		return sessions[i], nil
	}, &calls
}

// newConnectedTestConnection builds a Connection whose dial succeeds, with the
// topology stubbed out because there is no broker to declare against. The dial is
// the caller's, so a test can hand out a different session per call.
//
// No t.Cleanup is registered: the channels handed out are zero &amqp.Channel{}, and
// Connection.Close() on one of those panics inside the AMQP client unless the
// channel was already marked closed. That omission is the real cost of having no
// amqpChannel seam — it is why Connection.Close() has no test of its own. The seam
// (an interface covering ExchangeDeclare/QueueDeclare/Consume/Publish/Qos/Close)
// and the coverage it would unlock are on the backlog, deliberately not built here.
func newConnectedTestConnection(t *testing.T, dial dialFunc) *Connection {
	t.Helper()
	c := newConnection("amqp://guest:guest@rabbitmq:5672/", dial)
	c.reconnectDelay = 0
	c.topology = func() error { return nil }
	return c
}

// --- connect / retry ---

// TestChannelRetriesAfterFailedDial is the regression test for the lazy-connect
// design: a failed dial must not poison every later call. If someone replaces the
// sync.Mutex with a naive sync.Once, the second Channel() never reaches dial again
// and this fails.
func TestChannelRetriesAfterFailedDial(t *testing.T) {
	c, rec := newTestConnection(t, 0) // reconnectDelay 0 = negative cache disabled

	for i := 1; i <= 2; i++ {
		if _, err := c.Channel(); err == nil {
			t.Fatalf("call %d: Channel() = nil error, want a dial failure", i)
		}
	}

	if got := rec.calls.Load(); got != 2 {
		t.Errorf("dial calls = %d, want 2 (reconnectDelay=0 must let every call re-dial)", got)
	}
	// The URL is the only thing the caller configures; a hardcoded or stale one
	// would dial the wrong broker and still pass every other assertion here.
	for i, got := range rec.urls {
		if got != c.url {
			t.Errorf("dial call %d: url = %q, want the connection's url %q", i+1, got, c.url)
		}
	}
}

// TestChannelNegativeCacheSkipsRedial proves the cache does three things at once:
// it suppresses a dial storm while the broker is down, it does not poison
// recovery once the window has passed, and it does not swallow the root cause.
func TestChannelNegativeCacheSkipsRedial(t *testing.T) {
	t.Run("suppresses the redial and keeps the cause", func(t *testing.T) {
		c, rec := newTestConnection(t, 5*time.Second)

		if _, err := c.Channel(); err == nil {
			t.Fatal("first Channel(): want a dial failure, got nil error")
		}

		cached, err := c.Channel()
		if err == nil {
			t.Fatal("second Channel(): want the cached error, got nil error")
		}
		if cached != nil {
			t.Errorf("second Channel() channel = %v, want nil on failure", cached)
		}
		if got := rec.calls.Load(); got != 1 {
			t.Fatalf("dial calls = %d, want 1 (the negative cache must suppress the re-dial)", got)
		}
		// The operator loses the reason exactly when the message goes generic.
		// errors.Is must still reach the cause through the cache.
		if !errors.Is(err, errRefused) {
			t.Errorf("cached error = %v, want it to wrap the root cause %v", err, errRefused)
		}
		if !strings.Contains(err.Error(), "RabbitMQ unavailable") {
			t.Errorf("cached error = %v, want the 'unavailable' prefix", err)
		}
	})

	t.Run("expires on its own", func(t *testing.T) {
		const delay = 20 * time.Millisecond
		c, rec := newTestConnection(t, delay)

		if _, err := c.Channel(); err == nil {
			t.Fatal("first Channel(): want a dial failure, got nil error")
		}
		if _, err := c.Channel(); err == nil {
			t.Fatal("second Channel(): want the cached error, got nil error")
		}
		if got := rec.calls.Load(); got != 1 {
			t.Fatalf("dial calls = %d, want 1 inside the cache window", got)
		}

		time.Sleep(4 * delay) // the cache window has now elapsed on its own
		if _, err := c.Channel(); err == nil {
			t.Fatal("Channel() after the window: want a dial failure, got nil error")
		}
		if got := rec.calls.Load(); got != 2 {
			t.Errorf("dial calls = %d, want 2 once the cache window elapsed", got)
		}
	})

	t.Run("is escapable without waiting", func(t *testing.T) {
		c, rec := newTestConnection(t, 5*time.Second)
		if _, err := c.Channel(); err == nil {
			t.Fatal("first Channel(): want a dial failure, got nil error")
		}

		// Pretend the failure happened long ago. Deterministic, and unlike a sleep
		// it does not slow the suite or flake under -count=30.
		c.mu.Lock()
		c.lastDialFail = time.Now().Add(-10 * time.Second)
		c.mu.Unlock()

		if _, err := c.Channel(); err == nil {
			t.Fatal("Channel() after expiring the cache: want a dial failure, got nil error")
		}
		if got := rec.calls.Load(); got != 2 {
			t.Errorf("dial calls = %d, want 2 once the negative cache expired", got)
		}
	})
}

// TestChannelRetriesAfterTopologyFailure locks in that a failed topology
// declaration is not sticky. This is the regression test for the bug where a
// sync.Once fired on the error and returned the same failure for the process
// lifetime. Reintroduce the Once and this test fails on the second Channel().
func TestChannelRetriesAfterTopologyFailure(t *testing.T) {
	session := &fakeSession{ch: &amqp.Channel{}}
	c := newConnectedTestConnection(t, staticDial(session))

	var attempts atomic.Int64
	c.topology = func() error {
		if attempts.Add(1) == 1 {
			return errors.New("PRECONDITION_FAILED - inequivalent arg 'x-max-priority'")
		}
		return nil
	}

	if _, err := c.Channel(); err == nil {
		t.Fatal("first Channel(): want the topology failure to propagate")
	}
	c.mu.Lock()
	sticky := c.topologyDeclared
	c.mu.Unlock()
	if sticky {
		t.Error("topologyDeclared = true after a failed declaration; the error must not be sticky")
	}

	ch, err := c.Channel()
	if err != nil {
		t.Fatalf("second Channel() = %v, want success: a failed declaration must stay retryable", err)
	}
	if ch != session.ch {
		t.Errorf("channel = %p, want the session's channel %p", ch, session.ch)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("topology attempts = %d, want 2", got)
	}
	c.mu.Lock()
	declared := c.topologyDeclared
	c.mu.Unlock()
	if !declared {
		t.Error("topologyDeclared = false after a successful declaration")
	}

	// A third call must reuse the declaration instead of re-running it.
	if _, err := c.Channel(); err != nil {
		t.Fatalf("third Channel() = %v, want the cached topology", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("topology attempts = %d, want 2: a declared topology must not be re-declared", got)
	}
}

// TestChannelConnectRollsBackOnChannelError covers the branch where the TCP
// session opens but the AMQP channel cannot be created: nothing may be left
// half-installed, and the cause must survive.
func TestChannelConnectRollsBackOnChannelError(t *testing.T) {
	openErr := errors.New("channel_max exceeded")
	session := &fakeSession{chErr: openErr}
	c := newConnectedTestConnection(t, staticDial(session))
	t.Cleanup(func() { _ = c.Close() }) // c.channel stays nil, so this is safe

	_, err := c.Channel()
	if err == nil {
		t.Fatal("Channel() = nil error, want the channel-open failure")
	}
	if !errors.Is(err, openErr) {
		t.Errorf("error = %v, want it to wrap the cause %v", err, openErr)
	}

	c.mu.Lock()
	sess, ch, declared := c.session, c.channel, c.topologyDeclared
	c.mu.Unlock()
	if sess != nil {
		t.Errorf("c.session = %v, want nil after a failed channel open", sess)
	}
	if ch != nil {
		t.Errorf("c.channel = %v, want nil after a failed channel open", ch)
	}
	if declared {
		t.Error("topologyDeclared = true after a failed connect")
	}
	if got := session.closes(); got != 1 {
		t.Errorf("session Close calls = %d, want 1: a session that opened no channel must be closed", got)
	}
}

// TestChannelNilChannelIsRejected covers a session that reports success but hands
// back no channel. Without the guard the nil *amqp.Channel reaches declareTopology
// and panics inside the AMQP client.
func TestChannelNilChannelIsRejected(t *testing.T) {
	session := &fakeSession{} // Channel() returns (nil, nil)
	c := newConnectedTestConnection(t, staticDial(session))
	t.Cleanup(func() { _ = c.Close() })

	ch, err := c.Channel()
	if err == nil {
		t.Fatalf("Channel() = (%v, nil), want an error for a nil channel", ch)
	}
	if ch != nil {
		t.Errorf("channel = %v, want nil", ch)
	}
	if !errors.Is(err, errNilChannel) {
		t.Errorf("error = %v, want errNilChannel", err)
	}
	if got := session.closes(); got != 1 {
		t.Errorf("session Close calls = %d, want 1: a session with no channel must be closed", got)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.channel != nil {
		t.Errorf("c.channel = %v, want nil", c.channel)
	}
	if c.session != nil {
		t.Errorf("c.session = %v, want nil", c.session)
	}
}

// TestChannelReconnectReleasesTheOldSession covers the two halves of the
// reconnect path. A broker restart leaves a dead session behind: the old one
// must be closed, or every restart leaks a socket and its reader goroutine, and
// the fresh channel must have the topology declared again — otherwise the next
// Publish gets 404 NOT_FOUND on an exchange that does not exist on that channel.
//
// The dial hands out a DIFFERENT session on the second call on purpose. A dial
// that returned the same fake every time would satisfy "a new session was
// installed" and "the old one was closed" with the original object, so the
// reconnect could install nothing at all and the test would still pass.
func TestChannelReconnectReleasesTheOldSession(t *testing.T) {
	first := &fakeSession{ch: &amqp.Channel{}}
	second := &fakeSession{ch: &amqp.Channel{}}
	dial, dials := dialSequence(first, second)
	c := newConnectedTestConnection(t, dial)

	var declarations atomic.Int64
	c.topology = func() error { declarations.Add(1); return nil }

	if _, err := c.Channel(); err != nil {
		t.Fatalf("first Channel() = %v, want a connect", err)
	}
	if got := declarations.Load(); got != 1 {
		t.Fatalf("topology declarations = %d, want 1", got)
	}

	// The broker dropped us: the session now reports itself closed.
	first.mu.Lock()
	first.closed = true
	first.mu.Unlock()

	ch, err := c.Channel()
	if err != nil {
		t.Fatalf("Channel() after the session died = %v, want a reconnect", err)
	}
	if ch != second.ch {
		t.Errorf("channel = %p, want the second session's channel %p", ch, second.ch)
	}
	if got := first.closes(); got != 1 {
		t.Errorf("first session Close calls = %d, want 1: a replaced session must be closed", got)
	}
	if got := second.closes(); got != 0 {
		t.Errorf("second session Close calls = %d, want 0: the live session must not be closed", got)
	}
	if got := dials.Load(); got != 2 {
		t.Errorf("dial calls = %d, want 2: a dead session must be redialled", got)
	}
	if got := declarations.Load(); got != 2 {
		t.Errorf("topology declarations = %d, want 2: a fresh channel has no exchanges and needs its own declaration", got)
	}
}

// TestChannelReopensWhenTheChannelDies is the regression test for a hole in the
// session-only liveness check: the broker kills the CHANNEL, not the connection,
// whenever a declaration is rejected with PRECONDITION_FAILED — a queue declared
// with arguments that diverge from the broker's own view is the usual cause. The
// session stays perfectly healthy, so a check on IsClosed() of the session alone
// kept serving the dead channel: every Publish then returned amqp.ErrClosed for
// the rest of the process' life, with no redial, no re-declared topology and no
// error from Channel() to show for it.
func TestChannelReopensWhenTheChannelDies(t *testing.T) {
	var issued []*amqp.Channel
	session := &fakeSession{makeCh: func() *amqp.Channel {
		ch := &amqp.Channel{}
		issued = append(issued, ch) // the test drives this single-threaded
		return ch
	}}
	dial, dials := dialSequence(session)
	c := newConnectedTestConnection(t, dial)

	var declarations atomic.Int64
	c.topology = func() error { declarations.Add(1); return nil }

	ch, err := c.Channel()
	if err != nil {
		t.Fatalf("first Channel() = %v, want a connect", err)
	}
	dead := ch

	// The broker killed the channel; the session never noticed.
	markChannelClosed(dead)
	if !dead.IsClosed() {
		t.Fatal("markChannelClosed did not take: the test is not exercising the dead-channel path")
	}

	ch, err = c.Channel()
	if err != nil {
		t.Fatalf("Channel() after the channel died = %v, want a reopen", err)
	}
	if ch == dead {
		t.Fatal("Channel() handed back the closed channel; every Publish from here on would be amqp.ErrClosed")
	}
	if ch.IsClosed() {
		t.Error("Channel() handed back a closed channel")
	}
	if got := dials.Load(); got != 1 {
		t.Errorf("dial calls = %d, want 1: the session was alive, so a reopen is enough and a redial is waste", got)
	}
	if got := declarations.Load(); got != 2 {
		t.Errorf("topology declarations = %d, want 2: the reopened channel is a new channel and declares its own topology", got)
	}
	if !dead.IsClosed() {
		t.Error("the replaced channel must not come back to life")
	}
	if got := len(issued); got != 2 {
		t.Errorf("channels issued = %d, want 2 (the original and its replacement)", got)
	}

	// A third call must reuse the live channel instead of reopening again.
	reused, err := c.Channel()
	if err != nil {
		t.Fatalf("third Channel() = %v, want the live channel", err)
	}
	if reused != ch {
		t.Error("Channel() reopened a channel that was still live")
	}
	if got := declarations.Load(); got != 2 {
		t.Errorf("topology declarations = %d, want 2: a declared topology must not be re-declared", got)
	}
}

// TestChannelReopenRejectsNilChannel is the nil-channel guard on the reopen path.
// The dial path has its own; without one here a session that opens fine and then
// reports no channel would install a nil *amqp.Channel, and declareTopology would
// dereference it inside the AMQP client.
func TestChannelReopenRejectsNilChannel(t *testing.T) {
	opened := 0
	session := &fakeSession{makeCh: func() *amqp.Channel {
		opened++
		if opened == 1 {
			return &amqp.Channel{}
		}
		return nil // the reopen hands back nothing
	}}
	c := newConnectedTestConnection(t, staticDial(session))
	// The reopen fails, so the connection still holds a zero &amqp.Channel{} that
	// the cleanup would panic on. Marking it closed first turns Close into the
	// documented no-op.
	t.Cleanup(func() {
		c.mu.Lock()
		held := c.channel
		c.mu.Unlock()
		if held != nil {
			markChannelClosed(held)
		}
		_ = c.Close()
	})

	if _, err := c.Channel(); err != nil {
		t.Fatalf("first Channel() = %v, want a connect", err)
	}

	c.mu.Lock()
	live := c.channel
	c.mu.Unlock()
	markChannelClosed(live)

	ch, err := c.Channel()
	if err == nil {
		t.Fatalf("Channel() = (%v, nil), want an error for a nil channel on reopen", ch)
	}
	if !errors.Is(err, errNilChannel) {
		t.Errorf("error = %v, want errNilChannel", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.channel != live {
		t.Errorf("c.channel = %p, want the dead channel left in place: a rejected reopen must not install nil", c.channel)
	}
	if c.topologyDeclared {
		t.Error("topologyDeclared = true after a rejected reopen")
	}
}

// TestChannelRejectsNilSession covers a dial that reports success but hands back
// nothing at all. The guard turns that into an error instead of a nil-interface
// dereference inside connectLocked.
func TestChannelRejectsNilSession(t *testing.T) {
	c := newConnection("amqp://guest:guest@rabbitmq:5672/", func(string) (amqpSession, error) {
		return nil, nil
	})
	c.reconnectDelay = 0
	c.topology = func() error { return nil }
	t.Cleanup(func() { _ = c.Close() })

	ch, err := c.Channel()
	if err == nil {
		t.Fatalf("Channel() = (%v, nil), want an error for a nil session", ch)
	}
	if !errors.Is(err, errNilSession) {
		t.Errorf("error = %v, want errNilSession", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil || c.channel != nil {
		t.Error("a rejected session must not be installed")
	}
}

// TestAmqpDialRejectsMalformedURL pins the production adapter. A bad URL fails in
// the parser, with no socket and no DNS.
//
// It also records a trap: amqpDial hands back the typed-nil *amqp.Connection as a
// NON-nil amqpSession, because that is how Go boxes a nil pointer into an
// interface. connectLocked is safe because it checks err before it ever looks at
// the session — this test is the reminder that the order matters.
func TestAmqpDialRejectsMalformedURL(t *testing.T) {
	// amqp.Dial never returns (nil, nil), so there is no session to close and no
	// cleanup worth writing: a defensive "close it anyway" here would panic inside
	// the AMQP client instead of failing the test cleanly.
	session, err := amqpDial("amqp://%zz:not-a-url@127.0.0.1:5672/")
	if err == nil {
		t.Fatal("amqpDial() = nil error for a malformed URL, want a parse failure")
	}
	// The trap, made concrete: today the session is a non-nil interface wrapping a
	// nil *amqp.Connection, so errNilSession in connectLocked does NOT fire and the
	// err-before-session ordering is the only thing that saves us. If a future
	// amqp091 returns a true nil interface on failure, the sentinel starts to matter.
	if session == nil {
		t.Log("amqpDial returned a nil interface with a non-nil error; errNilSession is now load-bearing")
	}
	if !strings.Contains(err.Error(), "URL") && !strings.Contains(err.Error(), "url") {
		t.Errorf("amqpDial() = %v, want a URL parse failure", err)
	}
}

// TestCloseConcurrentWithChannel drives Channel() and Close() at the same time over
// a connection that is actually connected, so the fields the assertions below read
// were installed and cleared for real instead of being described in a comment about
// fields that were never set.
//
// The fake session reports itself alive and hands out already-closed channels: the
// reconnect decision then stays on the reopen path, and Close() reaches a channel
// whose Close is a documented no-op instead of a nil dereference.
func TestCloseConcurrentWithChannel(t *testing.T) {
	const (
		channelers = 8
		closers    = 4
		rounds     = 50
	)
	quietLogs(t) // one log line per connect otherwise, thousands under -count=50
	session := deadChannelSession()
	var dials atomic.Int64
	c := newConnectedTestConnection(t, func(string) (amqpSession, error) {
		dials.Add(1)
		return session, nil
	})

	var wg sync.WaitGroup
	wg.Add(channelers + closers)
	for i := 0; i < channelers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				_, _ = c.Channel() // error is irrelevant here
			}
		}()
	}
	for i := 0; i < closers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				_ = c.Close()
			}
		}()
	}
	wg.Wait() // no goroutine outlives this line, so every assertion below is stable

	// One more Close makes the post-condition deterministic. Without it the last
	// goroutine to run decides the state, and a regression in what Close clears
	// would only be caught in the runs where a Channel() happened to go last.
	if err := c.Close(); err != nil {
		t.Errorf("Close() = %v, want no error from a fake session and a closed channel", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil || c.channel != nil {
		t.Error("Close must leave session and channel nil, so a later Channel() reconnects")
	}
	if c.topologyDeclared {
		t.Error("Close must clear topologyDeclared along with the channel")
	}
	// The two assertions above are only worth anything if the fields were non-nil
	// in the first place. This is the guard against that: a fake that stopped
	// connecting would turn them into decoration again.
	if got := dials.Load(); got == 0 {
		t.Error("the dial never succeeded, so nothing was ever installed and the assertions above are vacuous")
	}
	if got := session.closes(); got == 0 {
		t.Error("the session was never closed: Close must release the connection it stole, not drop it")
	}
}

// blockingSession is a fakeSession whose Close() parks until the test lets it go.
// It stands in for the AMQP close handshake, which is a round trip with no timeout
// of its own — the reason Connection.Close() releases c.mu before closing anything.
type blockingSession struct {
	*fakeSession
	entered     chan struct{} // closed when Close is entered
	release     chan struct{} // closed by the test to let Close return
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func newBlockingSession() *blockingSession {
	return &blockingSession{
		fakeSession: deadChannelSession(),
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}
}

func (b *blockingSession) Close() error {
	b.enterOnce.Do(func() { close(b.entered) })
	<-b.release
	return b.fakeSession.Close()
}

// releaseNow lets a parked Close return. Idempotent, so it is safe to call from a
// defer on the failure path and from the cleanup.
func (b *blockingSession) releaseNow() { b.releaseOnce.Do(func() { close(b.release) }) }

// TestCloseDoesNotHoldTheLockDuringTheCloseHandshake is the test that makes the
// lock discipline in Connection.Close() observable. The AMQP close handshake is a
// round trip with no timeout, so holding c.mu across it parks every Channel() — and
// therefore every Publish, including the ones serving an HTTP request — for as long
// as a wedged broker takes to answer.
//
// This is the one property the concurrent test above cannot see: it passes whether
// or not the lock is held across the close, because nothing in it blocks long enough
// to notice. Here the session's Close() blocks on purpose and a second Channel() has
// to complete anyway.
func TestCloseDoesNotHoldTheLockDuringTheCloseHandshake(t *testing.T) {
	quietLogs(t)
	session := newBlockingSession()
	c := newConnectedTestConnection(t, staticDial(session))
	// Unblocks the parked Close on the failure path too, so a regression fails the
	// test instead of hanging the binary.
	defer session.releaseNow()
	t.Cleanup(func() {
		session.releaseNow()
		_ = c.Close()
	})

	if _, err := c.Channel(); err != nil {
		t.Fatalf("first Channel() = %v, want a connect", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()

	select {
	case <-session.entered:
	case <-time.After(5 * time.Second):
		session.releaseNow()
		<-closed
		t.Fatal("Close never reached the AMQP handshake")
	}

	// Close is now parked inside session.Close() with the fields already stolen.
	// Channel() has to be free to run: the connection is closed, so this dials again.
	served := make(chan struct{})
	go func() {
		_, _ = c.Channel()
		close(served)
	}()

	blocked := false
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		blocked = true
	}
	// Release before asserting, so no goroutine is left parked on a failing run.
	session.releaseNow()
	if err := <-closed; err != nil {
		t.Errorf("Close() = %v, want no error", err)
	}
	<-served
	if blocked {
		t.Error("Channel() was parked while Close() was in the AMQP handshake: c.mu is held across a round trip with no timeout")
	}
}

// TestChannelConcurrentFirstUse drives 16 goroutines into the lazy path at once.
// It matters because -race only reports a race on code the test actually runs.
//
// The fake dial always fails, so Channel() returns before it can touch
// c.channel. The mutex is still fully exercised — 16 goroutines contend for it
// and each one then reads/writes the negative-cache fields under it.
func TestChannelConcurrentFirstUse(t *testing.T) {
	const goroutines = 16
	c, rec := newTestConnection(t, 0) // every goroutine must reach the dial

	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.Channel()
		}(i)
	}
	wg.Wait() // nothing below may read errs while a goroutine still writes it

	for i, err := range errs {
		if err == nil {
			t.Errorf("goroutine %d: Channel() = nil error, want a dial failure", i)
		}
	}
	if got := rec.calls.Load(); got != goroutines {
		t.Errorf("dial calls = %d, want %d (reconnectDelay=0: no goroutine may be short-circuited)", got, goroutines)
	}
}

// TestNewConnectionDoesNotDial locks in the lazy decision: the constructor must not
// touch the network, so a broker that is down at boot can no longer stop the
// process from starting.
//
// The dial is injected into newConnection rather than assigned onto the returned
// value. Assigning afterwards would make the assertion unreachable — the fake
// could not have been called by a constructor that had already returned.
func TestNewConnectionDoesNotDial(t *testing.T) {
	rec := &dialRecorder{err: errRefused}
	c := newConnection("amqp://guest:guest@127.0.0.1:1/", rec.dial)
	t.Cleanup(func() { _ = c.Close() })

	if got := rec.calls.Load(); got != 0 {
		t.Errorf("dial calls = %d, want 0: the constructor must not dial", got)
	}
	if c.topology == nil {
		t.Error("the constructor must wire the topology seam")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil || c.channel != nil {
		t.Error("the constructor must leave session and channel nil")
	}
	if !c.lastDialFail.IsZero() {
		t.Error("the constructor must not record a dial failure")
	}
}

// TestNewConnectionWiresTheProductionAdapter covers the exported constructor the
// rest of the program actually calls. It must build a usable Connection without
// dialling, and it must install the real dial and topology seams.
func TestNewConnectionWiresTheProductionAdapter(t *testing.T) {
	c := NewConnection("amqp://guest:guest@127.0.0.1:1/")
	t.Cleanup(func() { _ = c.Close() })

	if c.dial == nil || c.topology == nil {
		t.Fatal("NewConnection must wire both the dial and the topology seams")
	}
	if c.url != "amqp://guest:guest@127.0.0.1:1/" {
		t.Errorf("url = %q, want the constructor argument", c.url)
	}
	if c.reconnectDelay != 5*time.Second {
		t.Errorf("reconnectDelay = %s, want 5s", c.reconnectDelay)
	}
}

// --- error propagation to callers ---

// TestPublishPropagatesChannelError is the regression test for the error branch
// Publish gained when the channel became lazy. It is the path an HTTP handler
// takes when the broker is down: without it, Publish would have no coverage and
// a regression there would be invisible.
func TestPublishPropagatesChannelError(t *testing.T) {
	c, _ := newTestConnection(t, 0)
	p := NewPublisher(c)

	err := p.Publish(ExchangeKeyEvents, RoutingKeyKeyCreated,
		KeyCreatedEvent{ID: 1, Alias: "a", KeySize: 2048, Timestamp: time.Now()})
	if err == nil {
		t.Fatal("Publish() = nil, want the channel failure")
	}
	if !strings.Contains(err.Error(), "failed to get channel") {
		t.Errorf("Publish() = %v, want the 'failed to get channel' context", err)
	}
	if !errors.Is(err, errRefused) {
		t.Errorf("Publish() = %v, want it to wrap the root cause %v", err, errRefused)
	}
}

// TestPublishRejectsUnmarshalableEvent covers the guard that runs before the
// channel is even asked for, so an event that cannot be encoded never turns into
// a broker round trip.
func TestPublishRejectsUnmarshalableEvent(t *testing.T) {
	c, rec := newTestConnection(t, 0)

	err := NewPublisher(c).Publish(ExchangeKeyEvents, RoutingKeyKeyCreated, make(chan int))
	if err == nil {
		t.Fatal("Publish() = nil, want a marshal failure")
	}
	if !strings.Contains(err.Error(), "failed to marshal event") {
		t.Errorf("Publish() = %v, want the 'failed to marshal event' context", err)
	}
	if got := rec.calls.Load(); got != 0 {
		t.Errorf("dial calls = %d, want 0: a marshal failure must not reach the broker", got)
	}
}

// TestConsumePropagatesChannelError is the same regression for Consume.
func TestConsumePropagatesChannelError(t *testing.T) {
	c, _ := newTestConnection(t, 0)
	cons := NewConsumer(c)

	err := cons.Consume("q.entropy.new", func([]byte) error { return nil })
	if err == nil {
		t.Fatal("Consume() = nil, want the channel failure")
	}
	if !errors.Is(err, errRefused) {
		t.Errorf("Consume() = %v, want it to wrap the root cause %v", err, errRefused)
	}
}

// TestConsumeWithPrefetchPropagatesChannelError is the same regression for the
// prefetch variant, which reaches Channel() before the Qos call.
func TestConsumeWithPrefetchPropagatesChannelError(t *testing.T) {
	c, _ := newTestConnection(t, 0)
	cons := NewConsumer(c)

	err := cons.ConsumeWithPrefetch("q.entropy.new", 10, func([]byte) error { return nil })
	if err == nil {
		t.Fatal("ConsumeWithPrefetch() = nil, want the channel failure")
	}
	if !errors.Is(err, errRefused) {
		t.Errorf("ConsumeWithPrefetch() = %v, want it to wrap the root cause %v", err, errRefused)
	}
}
