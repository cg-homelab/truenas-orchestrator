package truenas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

type rawRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// wsServer runs handler for each websocket connection on a loopback httptest server.
func wsServer(t *testing.T, opts *websocket.AcceptOptions, handler func(ctx context.Context, ws *websocket.Conn)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, opts)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer func() { _ = ws.CloseNow() }()
		handler(r.Context(), ws)
	}))
	t.Cleanup(srv.Close)
	return "ws://" + srv.Listener.Addr().String()
}

func dialConn(t *testing.T, url string, interval time.Duration) *conn {
	t.Helper()
	return dialConnTimings(t, url, connTimings{pingInterval: interval})
}

func dialConnTimings(t *testing.T, url string, timings connTimings) *conn {
	t.Helper()
	hc, raw := httpClient(Config{})
	ws, resp, err := websocket.Dial(t.Context(), url, &websocket.DialOptions{HTTPClient: hc})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("websocket.Dial() error = %v", err)
	}
	c := newConn(t.Context(), ws, raw, slog.New(slog.DiscardHandler), timings)
	t.Cleanup(func() { _ = c.close() })
	return c
}

// drain keeps reading until the connection ends, so the client's close handshake is answered.
func drain(ctx context.Context, ws *websocket.Conn) {
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
	}
}

func readRequest(ctx context.Context, ws *websocket.Conn) (rawRequest, error) {
	_, data, err := ws.Read(ctx)
	if err != nil {
		return rawRequest{}, err
	}
	var r rawRequest
	err = json.Unmarshal(data, &r)
	return r, err
}

func writeJSON(ctx context.Context, ws *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return ws.Write(ctx, websocket.MessageText, b)
}

func TestConnConcurrentCallsGetTheirOwnReplies(t *testing.T) {
	const n = 10
	url := wsServer(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		var reqs []rawRequest
		for range n {
			r, err := readRequest(ctx, ws)
			if err != nil {
				return
			}
			reqs = append(reqs, r)
		}
		for i := len(reqs) - 1; i >= 0; i-- { // answer out of order
			_ = writeJSON(ctx, ws, map[string]any{"jsonrpc": "2.0", "id": reqs[i].ID, "result": reqs[i].Method})
		}
		drain(ctx, ws)
	})
	c := dialConn(t, url, 0)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			method := fmt.Sprintf("m.%d", i)
			raw, err := c.call(t.Context(), method, nil)
			if err != nil {
				t.Errorf("call(%s) error = %v", method, err)
				return
			}
			if got := string(raw); got != `"`+method+`"` {
				t.Errorf("call(%s) = %s, want %q", method, got, method)
			}
		})
	}
	wg.Wait()
}

func TestConnParamsAreAlwaysAnArray(t *testing.T) {
	got := make(chan string, 2)
	url := wsServer(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		for {
			r, err := readRequest(ctx, ws)
			if err != nil {
				return
			}
			got <- string(r.Params)
			_ = writeJSON(ctx, ws, map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": nil})
		}
	})
	c := dialConn(t, url, 0)
	if _, err := c.call(t.Context(), "a", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.call(t.Context(), "b", []any{"/mnt"}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{`[]`, `["/mnt"]`}, []string{<-got, <-got}); diff != "" {
		t.Errorf("params mismatch (-want +got):\n%s", diff)
	}
}

func TestConnCancelAbandonsReply(t *testing.T) {
	release := make(chan struct{})
	url := wsServer(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		var slowID json.RawMessage
		for {
			r, err := readRequest(ctx, ws)
			if err != nil {
				return
			}
			switch r.Method {
			case "slow":
				slowID = r.ID
			case "release":
				<-release
				// The abandoned reply arrives late, then the release reply.
				_ = writeJSON(ctx, ws, map[string]any{"jsonrpc": "2.0", "id": slowID, "result": "late"})
				_ = writeJSON(ctx, ws, map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": "released"})
			default:
				_ = writeJSON(ctx, ws, map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": r.Method})
			}
		}
	})
	c := dialConn(t, url, 0)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := c.call(ctx, "slow", nil)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond) // let the request go out
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("call(slow) after cancel error = %v, want context.Canceled", err)
	}
	if raw, err := c.call(t.Context(), "fast", nil); err != nil || string(raw) != `"fast"` {
		t.Fatalf("call(fast) after a cancelled call = %s, %v", raw, err)
	}
	close(release)
	if raw, err := c.call(t.Context(), "release", nil); err != nil || string(raw) != `"released"` {
		t.Fatalf("call(release) = %s, %v, want the late reply dropped", raw, err)
	}
	if _, err := c.call(ctx, "x", nil); !errors.Is(err, context.Canceled) {
		t.Errorf("call(cancelled ctx) error = %v, want context.Canceled", err)
	}
}

func TestConnDropsNotificationsAndStrayMessages(t *testing.T) {
	url := wsServer(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		r, err := readRequest(ctx, ws)
		if err != nil {
			return
		}
		_ = writeJSON(ctx, ws, map[string]any{"jsonrpc": "2.0", "method": "collection_update", "params": map[string]any{"msg": "added"}})
		_ = writeJSON(ctx, ws, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "Parse error"}})
		_ = writeJSON(ctx, ws, map[string]any{"jsonrpc": "2.0", "id": "foreign", "result": 1})
		_ = writeJSON(ctx, ws, map[string]any{"jsonrpc": "2.0", "id": 99999, "result": 1})
		_ = ws.Write(ctx, websocket.MessageText, []byte("not json"))
		_ = ws.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3})
		_ = writeJSON(ctx, ws, map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": "ok"})
		drain(ctx, ws)
	})
	c := dialConn(t, url, 0)
	if raw, err := c.call(t.Context(), "m", nil); err != nil || string(raw) != `"ok"` {
		t.Fatalf("call() = %s, %v, want \"ok\"", raw, err)
	}
}

func TestConnCloseFailsPendingCalls(t *testing.T) {
	url := wsServer(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		for {
			if _, err := readRequest(ctx, ws); err != nil {
				return // never replies
			}
		}
	})
	c := dialConn(t, url, 0)
	done := make(chan error, 1)
	go func() {
		_, err := c.call(t.Context(), "hang", nil)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := c.close(); err != nil {
		t.Errorf("close() error = %v", err)
	}
	if err := c.close(); err != nil {
		t.Errorf("second close() error = %v", err)
	}
	if err := <-done; !errors.Is(err, homelab.ErrUnavailable) {
		t.Errorf("pending call after close error = %v, want ErrUnavailable", err)
	}
	if _, err := c.call(t.Context(), "after", nil); !errors.Is(err, homelab.ErrUnavailable) {
		t.Errorf("call after close error = %v, want ErrUnavailable", err)
	}
}

func TestConnServerGone(t *testing.T) {
	url := wsServer(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		_, _ = readRequest(ctx, ws)
		_ = ws.CloseNow()
	})
	c := dialConn(t, url, 0)
	if _, err := c.call(t.Context(), "m", nil); !errors.Is(err, homelab.ErrUnavailable) {
		t.Errorf("call() on a dropped connection error = %v, want ErrUnavailable", err)
	}
}

func TestConnPings(t *testing.T) {
	var pings atomic.Int32
	url := wsServer(t, &websocket.AcceptOptions{
		OnPingReceived: func(context.Context, []byte) bool { pings.Add(1); return true },
	}, func(ctx context.Context, ws *websocket.Conn) {
		_, _, _ = ws.Read(ctx) // a reader must run for control frames to be answered
	})
	dialConn(t, url, 5*time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for pings.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("server saw %d pings, want at least 3", pings.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConnReadsLargeReplies(t *testing.T) {
	big := strings.Repeat("x", 1<<20) // far above coder/websocket's 32 KiB default
	url := wsServer(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		r, err := readRequest(ctx, ws)
		if err != nil {
			return
		}
		_ = writeJSON(ctx, ws, map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": big})
		drain(ctx, ws)
	})
	c := dialConn(t, url, 0)
	raw, err := c.call(t.Context(), "pool.dataset.query", nil)
	if err != nil {
		t.Fatalf("call() error = %v", err)
	}
	if len(raw) != len(big)+2 {
		t.Errorf("call() result length = %d, want %d", len(raw), len(big)+2)
	}
}

// TestConnMissingPongFailsCalls: coder/websocket's Ping only reports a missing pong, it does not
// close the connection, so the ping loop must, or a call to a dead peer hangs until TCP keepalive.
func TestConnMissingPongFailsCalls(t *testing.T) {
	url := wsServer(t, &websocket.AcceptOptions{
		OnPingReceived: func(context.Context, []byte) bool { return false }, // never pong
	}, func(ctx context.Context, ws *websocket.Conn) {
		drain(ctx, ws) // reads requests, never replies
	})
	c := dialConnTimings(t, url, connTimings{pingInterval: 5 * time.Millisecond, pingTimeout: 20 * time.Millisecond})
	done := make(chan error, 1)
	go func() {
		_, err := c.call(context.WithoutCancel(t.Context()), "hang", nil) // no deadline of its own
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, homelab.ErrUnavailable) {
			t.Errorf("call() after missing pongs error = %v, want ErrUnavailable", err)
		}
	case <-time.After(time.Second):
		t.Fatal("call() still waiting 1 s after the peer stopped answering pings")
	}
}

// TestConnCloseIsBounded: a peer that never answers the close frame must not hold close() for
// coder/websocket's 5 s handshake wait.
func TestConnCloseIsBounded(t *testing.T) {
	release := make(chan struct{})
	url := wsServer(t, nil, func(ctx context.Context, _ *websocket.Conn) {
		select { // no reader: the close frame is never answered
		case <-ctx.Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) }) // runs before the server's Close
	c := dialConnTimings(t, url, connTimings{closeTimeout: 50 * time.Millisecond})
	start := time.Now()
	if err := c.close(); err != nil {
		t.Errorf("close() error = %v, want nil", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("close() took %v against an unresponsive peer, want about the 50 ms timeout", d)
	}
	if _, err := c.call(t.Context(), "after", nil); !errors.Is(err, homelab.ErrUnavailable) {
		t.Errorf("call after close error = %v, want ErrUnavailable", err)
	}
}
