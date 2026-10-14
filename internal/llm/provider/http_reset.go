package provider

import (
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// connResetter is the HTTP transport a provider SDK client runs on. It exists
// so the RST_STREAM retry can really land on a new connection: with the SDK's
// default http.DefaultClient, a retry is multiplexed onto the same pooled
// HTTP/2 connection that just produced the reset (and a global transport must
// never be closed from here). Each connResetter owns a private transport,
// cloned from the process default, and Rotate swaps in a fresh clone so the
// next request dials a new TCP/TLS connection.
//
// Requests already in flight on the previous transport are left alone: only its
// idle connections are closed; a connection still carrying streams drains and
// is reaped by IdleConnTimeout once idle.
type connResetter struct {
	template *http.Transport
	cur      atomic.Pointer[connGeneration]
	mu       sync.Mutex
}

type connGeneration struct {
	id uint64
	tr *http.Transport
}

// newConnResetter builds a resetter around base, which is only ever cloned.
// A nil base starts from http.DefaultTransport's settings (proxy from
// environment, HTTP/2, dial and idle timeouts).
func newConnResetter(base *http.Transport) *connResetter {
	if base == nil {
		if dt, ok := http.DefaultTransport.(*http.Transport); ok {
			base = dt
		} else {
			base = &http.Transport{
				Proxy:             http.ProxyFromEnvironment,
				ForceAttemptHTTP2: true,
				IdleConnTimeout:   90 * time.Second,
			}
		}
	}
	c := &connResetter{template: base.Clone()}
	c.cur.Store(&connGeneration{id: 1, tr: c.template.Clone()})
	return c
}

// RoundTrip implements http.RoundTripper on the current generation.
func (c *connResetter) RoundTrip(req *http.Request) (*http.Response, error) {
	return c.cur.Load().tr.RoundTrip(req)
}

// Client returns an *http.Client without a timeout (streams are long-lived)
// that sends through the current transport generation.
func (c *connResetter) Client() *http.Client {
	return &http.Client{Transport: c}
}

// Generation reports the current connection generation; 0 on a nil resetter
// (clients built without one, e.g. in tests, keep the SDK default).
func (c *connResetter) Generation() uint64 {
	if c == nil {
		return 0
	}
	return c.cur.Load().id
}

// Rotate retires the generation a failed request observed and returns the
// generation the next request will use. When another goroutine already
// rotated past observed, it does not rotate again, so concurrent failures on
// the same dead connection produce one new transport rather than a cascade.
func (c *connResetter) Rotate(observed uint64) uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cur := c.cur.Load()
	if cur.id != observed {
		return cur.id
	}
	next := &connGeneration{id: cur.id + 1, tr: c.template.Clone()}
	c.cur.Store(next)
	cur.tr.CloseIdleConnections()
	return next.id
}

var streamIDPattern = regexp.MustCompile(`stream ID (\d+)`)

// streamIDFromError extracts the HTTP/2 stream id from an RST_STREAM error
// ("stream error: stream ID 17; INTERNAL_ERROR; received from peer"). The
// error type is unexported in net/http's bundled h2, so the message is the
// only carrier. Returns 0 when absent.
func streamIDFromError(err error) uint32 {
	if err == nil {
		return 0
	}
	m := streamIDPattern.FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	id, perr := strconv.ParseUint(m[1], 10, 32)
	if perr != nil {
		return 0
	}
	return uint32(id)
}

// rstRetryLog is what an RST_STREAM retry can observe about the reset and the
// connection the retry will use.
type rstRetryLog struct {
	streamID       uint32
	prevStreamID   uint32
	prevGeneration uint64
	connGeneration uint64
	newConn        bool
}

// rotateForRSTRetry retires the connection that produced err and describes the
// retry. prevStreamID is the stream id of the reset before this one (0 on the
// first retry); the new attempt's own stream id is unknowable until it fails,
// at which point it is reported as streamID of the next retry.
func rotateForRSTRetry(c *connResetter, observed uint64, prevStreamID uint32, err error) rstRetryLog {
	next := c.Rotate(observed)
	return rstRetryLog{
		streamID:       streamIDFromError(err),
		prevStreamID:   prevStreamID,
		prevGeneration: observed,
		connGeneration: next,
		newConn:        next != observed,
	}
}

func (l rstRetryLog) kv() []any {
	return []any{
		"stream_id", l.streamID,
		"previous_attempt_stream_id", l.prevStreamID,
		"previous_conn_generation", l.prevGeneration,
		"conn_generation", l.connGeneration,
		"new_conn", l.newConn,
	}
}
