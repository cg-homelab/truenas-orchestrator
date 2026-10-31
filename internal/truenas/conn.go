package truenas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

const (
	// readLimit is the largest message accepted. coder/websocket defaults to 32 KiB, and a dataset
	// query exceeds that.
	readLimit = 16 << 20
	// pingInterval keeps nginx (60 s proxy_read_timeout on /api) from closing an idle session.
	pingInterval = 20 * time.Second
	pingTimeout  = 10 * time.Second
	writeTimeout = 10 * time.Second
	// closeTimeout bounds the close handshake; an unresponsive peer is then dropped.
	closeTimeout = time.Second
)

// connTimings are the session's timers. A zero pingInterval disables pings; zero timeouts take
// the defaults.
type connTimings struct {
	pingInterval, pingTimeout, closeTimeout time.Duration
}

func defaultTimings() connTimings {
	return connTimings{pingInterval: pingInterval, pingTimeout: pingTimeout, closeTimeout: closeTimeout}
}

// conn is a JSON-RPC 2.0 session over one websocket. Calls may run concurrently; each waits for
// the reply with its id. A cancelled call abandons its reply (the middleware keeps running it).
type conn struct {
	ws     *websocket.Conn
	raw    *rawConn // the socket under ws; nil when unknown
	logger *slog.Logger
	nextID atomic.Uint64

	mu      sync.Mutex
	pending map[uint64]chan reply
	err     error // why the read loop stopped; set once

	pingTimeout  time.Duration
	closeTimeout time.Duration

	stop      context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// reply is what the read loop hands to a waiting call: a message, or the error that ended the
// session.
type reply struct {
	msg inbound
	err error
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

// inbound is any server message: a reply (id set) or a notification (method set, no id).
type inbound struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *wireError      `json:"error"`
}

// rawConn records the network connection a dial opened, so a stalled close can drop it: once
// coder/websocket's close handshake holds the read lock, only closing the socket ends its wait.
type rawConn struct {
	mu   sync.Mutex
	conn net.Conn
}

// dialContext wraps d so that every connection it opens is recorded.
func (r *rawConn) dialContext(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := d.DialContext(ctx, network, addr)
		if err == nil {
			r.mu.Lock()
			r.conn = c
			r.mu.Unlock()
		}
		return c, err
	}
}

// Close closes the recorded connection, if any.
func (r *rawConn) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return nil
	}
	return r.conn.Close()
}

// newConn takes ownership of ws and starts the read loop and, when t.pingInterval is positive,
// the ping loop. The loops outlive ctx's cancellation but keep its values; close stops them.
func newConn(ctx context.Context, ws *websocket.Conn, raw *rawConn, logger *slog.Logger, t connTimings) *conn {
	ws.SetReadLimit(readLimit)
	if t.pingTimeout <= 0 {
		t.pingTimeout = pingTimeout
	}
	if t.closeTimeout <= 0 {
		t.closeTimeout = closeTimeout
	}
	loopCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	c := &conn{
		ws: ws, raw: raw, logger: logger, pending: map[uint64]chan reply{}, stop: stop,
		pingTimeout: t.pingTimeout, closeTimeout: t.closeTimeout,
	}
	c.wg.Add(1)
	go c.readLoop(loopCtx)
	if t.pingInterval > 0 {
		c.wg.Add(1)
		go c.pingLoop(loopCtx, t.pingInterval)
	}
	return c
}

// call sends method with params (always a JSON array) and returns the raw result. A middleware
// error is an *Error; a transport failure wraps homelab.ErrUnavailable.
func (c *conn) call(ctx context.Context, method string, params []any) (json.RawMessage, error) {
	start := time.Now()
	msg, err := c.roundTrip(ctx, method, params)
	if err != nil {
		c.logger.DebugContext(ctx, "truenas call failed", "method", method, "duration", time.Since(start))
		return nil, err
	}
	if msg.Error != nil {
		e := newError(method, msg.Error)
		c.logger.DebugContext(ctx, "truenas call", "method", method, "duration", time.Since(start),
			"errname", e.name())
		return nil, e
	}
	c.logger.DebugContext(ctx, "truenas call", "method", method, "duration", time.Since(start))
	return msg.Result, nil
}

// roundTrip sends one request and waits for its reply message.
func (c *conn) roundTrip(ctx context.Context, method string, params []any) (inbound, error) {
	if err := ctx.Err(); err != nil {
		return inbound{}, fmt.Errorf("truenas: %s: %w", method, err)
	}
	if params == nil {
		params = []any{}
	}
	id := c.nextID.Add(1)
	ch := make(chan reply, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return inbound{}, lostError(method, err)
	}
	c.pending[id] = ch
	c.mu.Unlock()

	body, err := json.Marshal(request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		c.forget(id)
		return inbound{}, fmt.Errorf("truenas: %s: encode params: %w", method, err)
	}
	// The write ignores the caller's cancellation: coder/websocket closes the connection when a
	// write's context ends mid-frame, and one abandoned call must not end the session.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	err = c.ws.Write(wctx, websocket.MessageText, body)
	cancel()
	if err != nil {
		c.forget(id)
		return inbound{}, lostError(method, err)
	}

	select {
	case <-ctx.Done():
		c.forget(id)
		return inbound{}, fmt.Errorf("truenas: %s: %w", method, ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return inbound{}, lostError(method, r.err)
		}
		return r.msg, nil
	}
}

func lostError(method string, err error) error {
	return fmt.Errorf("truenas: %s: connection lost: %w: %w", method, homelab.ErrUnavailable, err)
}

func (c *conn) forget(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *conn) readLoop(ctx context.Context) {
	defer c.wg.Done()
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			c.fail(err)
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		c.dispatch(ctx, data)
	}
}

func (c *conn) dispatch(ctx context.Context, data []byte) {
	var msg inbound
	if err := json.Unmarshal(data, &msg); err != nil {
		c.logger.DebugContext(ctx, "truenas message dropped", "reason", "not json-rpc")
		return
	}
	if len(msg.ID) == 0 || string(msg.ID) == "null" {
		if msg.Method != "" {
			c.logger.DebugContext(ctx, "truenas notification dropped", "method", msg.Method)
		} else if msg.Error != nil {
			c.logger.DebugContext(ctx, "truenas error without id dropped", "code", msg.Error.Code)
		}
		return
	}
	id, err := strconv.ParseUint(string(msg.ID), 10, 64)
	if err != nil {
		c.logger.DebugContext(ctx, "truenas message dropped", "reason", "foreign id")
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ok {
		ch <- reply{msg: msg} // buffered; never blocks
	}
}

// fail records why the session ended and releases every waiting call.
func (c *conn) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
	}
	for id, ch := range c.pending {
		ch <- reply{err: c.err}
		delete(c.pending, id)
	}
}

func (c *conn) pingLoop(ctx context.Context, interval time.Duration) {
	defer c.wg.Done()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, c.pingTimeout)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				// A missing pong only fails the ping; the websocket stays open. Drop it, so the
				// read loop fails every waiting call instead of leaving them to TCP keepalive.
				if ctx.Err() == nil {
					c.logger.DebugContext(ctx, "truenas ping failed")
					_ = c.ws.CloseNow() // the read loop reports the loss
				}
				return
			}
		}
	}
}

// close performs the close handshake, bounded by closeTimeout, stops the loops and fails any
// waiting call. It is idempotent with closeNow; later calls return the first result.
func (c *conn) close() error { return c.shutdown(true) }

// closeNow drops the connection without a close handshake.
func (c *conn) closeNow() error { return c.shutdown(false) }

func (c *conn) shutdown(handshake bool) error {
	c.closeOnce.Do(func() {
		var err error
		forced := false // an unresponsive peer was dropped; its error is not the caller's
		if handshake {
			done := make(chan error, 1)
			go func() { done <- c.ws.Close(websocket.StatusNormalClosure, "") }()
			t := time.NewTimer(c.closeTimeout)
			select {
			case err = <-done:
				t.Stop()
			case <-t.C:
				// The peer does not answer. Drop the socket: that ends the handshake wait
				// whichever goroutine holds the read lock. Cancelling the read loop's context
				// does the same through coder/websocket when no socket was recorded.
				_ = c.raw.Close() // the forced close is the outcome; its error adds nothing
				c.stop()
				err = <-done
				forced = true
			}
		} else {
			err = c.ws.CloseNow()
		}
		c.stop()
		c.wg.Wait()
		c.fail(net.ErrClosed)
		var ce websocket.CloseError
		if !forced && err != nil && !errors.Is(err, net.ErrClosed) && !errors.As(err, &ce) {
			c.closeErr = fmt.Errorf("truenas: close: %w", err)
		}
	})
	return c.closeErr
}
