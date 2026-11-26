// Package jsonrpc is JSON-RPC 2.0 over a byte stream framed as the
// Language Server Protocol frames it: each message is a header of
// "Content-Length: n" lines, a blank line, and n bytes of JSON.
//
// A Conn reads messages in one goroutine. Notifications are handled in
// that goroutine, in the order they arrive, so that a notification that
// changes state (a document edit) is seen by every request after it.
// Requests are handled each in a goroutine of its own, with a context
// that $/cancelRequest cancels. Either side may send requests: Call
// sends one to the peer and waits for its response.
package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
)

// Error codes of JSON-RPC and LSP.
const (
	CodeParseError           = -32700
	CodeInvalidRequest       = -32600
	CodeMethodNotFound       = -32601
	CodeInvalidParams        = -32602
	CodeInternalError        = -32603
	CodeServerNotInitialized = -32002
	CodeRequestFailed        = -32803
	CodeRequestCancelled     = -32800
	CodeContentModified      = -32801
)

// Error is a JSON-RPC error object. A handler that returns one answers
// with it as it is; any other error is answered as an internal error.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message) }

// Errorf returns an *Error with the code and a formatted message.
func Errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrMethodNotFound is returned by handlers for methods they don't serve.
var ErrMethodNotFound = &Error{Code: CodeMethodNotFound, Message: "method not found"}

// ID is a request ID: a JSON number or string, kept as its JSON text.
type ID struct{ raw string }

// IntID returns the ID of the number n.
func IntID(n int64) ID { return ID{strconv.FormatInt(n, 10)} }

// String returns the ID's JSON text.
func (id ID) String() string { return id.raw }

// IsZero reports whether id is absent, as in a notification.
func (id ID) IsZero() bool { return id.raw == "" }

func (id ID) MarshalJSON() ([]byte, error) {
	if id.raw == "" {
		return []byte("null"), nil
	}
	return []byte(id.raw), nil
}

func (id *ID) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		id.raw = ""
		return nil
	}
	if s == "" || (s[0] != '"' && (s[0] < '0' || s[0] > '9') && s[0] != '-') {
		return fmt.Errorf("jsonrpc: invalid id %s", s)
	}
	id.raw = s
	return nil
}

// Request is a request or, without an ID, a notification.
type Request struct {
	ID     ID              `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether r has no ID.
func (r *Request) IsNotification() bool { return r.ID.IsZero() }

// wire is any message as it is on the wire.
type wire struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *ID             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Handler handles the peer's requests and notifications. For a
// notification its result is ignored, and an error is only logged.
type Handler func(ctx context.Context, conn *Conn, req *Request) (result any, err error)

// Conn is one JSON-RPC connection.
type Conn struct {
	r       *bufio.Reader
	w       io.Writer
	handler Handler
	logf    func(format string, args ...any)

	wmu sync.Mutex // serializes writes

	mu       sync.Mutex
	nextID   int64
	pending  map[string]chan *wire         // our calls awaiting a response
	inFlight map[string]context.CancelFunc // the peer's requests being handled
	done     chan struct{}                 // closed when Run returns
	closeErr error
	wg       sync.WaitGroup
}

// NewConn returns a connection reading from r and writing to w. logf, if
// not nil, logs errors that have no one to return them to.
func NewConn(r io.Reader, w io.Writer, handler Handler, logf func(string, ...any)) *Conn {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Conn{
		r:        bufio.NewReaderSize(r, 64<<10),
		w:        w,
		handler:  handler,
		logf:     logf,
		pending:  map[string]chan *wire{},
		inFlight: map[string]context.CancelFunc{},
		done:     make(chan struct{}),
	}
}

// Run reads and dispatches messages until the stream ends, ctx is done,
// or a message can't be framed. It waits for the requests being handled
// to finish. It returns nil at the end of the stream.
func (c *Conn) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	err := c.loop(ctx)
	cancel()
	c.wg.Wait()
	c.mu.Lock()
	c.closeErr = err
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = map[string]chan *wire{}
	c.mu.Unlock()
	close(c.done)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// Done is closed when Run has returned.
func (c *Conn) Done() <-chan struct{} { return c.done }

func (c *Conn) loop(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := readFrame(c.r)
		if err != nil {
			return err
		}
		var m wire
		if err := json.Unmarshal(body, &m); err != nil {
			c.reply(ID{}, nil, &Error{Code: CodeParseError, Message: err.Error()})
			continue
		}
		switch {
		case m.Method != "" && m.ID == nil:
			c.notification(ctx, &Request{Method: m.Method, Params: m.Params})
		case m.Method != "":
			c.request(ctx, &Request{ID: *m.ID, Method: m.Method, Params: m.Params})
		case m.ID != nil:
			c.mu.Lock()
			ch := c.pending[m.ID.raw]
			delete(c.pending, m.ID.raw)
			c.mu.Unlock()
			if ch != nil {
				ch <- &m
			}
		}
	}
}

func (c *Conn) notification(ctx context.Context, req *Request) {
	if req.Method == "$/cancelRequest" {
		var p struct {
			ID ID `json:"id"`
		}
		if json.Unmarshal(req.Params, &p) == nil {
			c.mu.Lock()
			if cancel := c.inFlight[p.ID.raw]; cancel != nil {
				cancel()
			}
			c.mu.Unlock()
		}
		return
	}
	if _, err := c.safeHandle(ctx, req); err != nil && !errors.Is(err, ErrMethodNotFound) {
		c.logf("notification %s: %v", req.Method, err)
	}
}

func (c *Conn) request(ctx context.Context, req *Request) {
	rctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.inFlight[req.ID.raw] = cancel
	c.mu.Unlock()
	c.wg.Go(func() {
		defer func() {
			c.mu.Lock()
			delete(c.inFlight, req.ID.raw)
			c.mu.Unlock()
			cancel()
		}()
		result, err := c.safeHandle(rctx, req)
		if err == nil && rctx.Err() != nil && ctx.Err() == nil {
			err = &Error{Code: CodeRequestCancelled, Message: "request cancelled"}
		}
		c.reply(req.ID, result, err)
	})
}

// safeHandle calls the handler, turning a panic into an internal error so
// that one bad request doesn't end the server.
func (c *Conn) safeHandle(ctx context.Context, req *Request) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			c.logf("panic handling %s: %v\n%s", req.Method, r, debug.Stack())
			result, err = nil, &Error{Code: CodeInternalError, Message: fmt.Sprintf("internal error: %v", r)}
		}
	}()
	return c.handler(ctx, c, req)
}

func (c *Conn) reply(id ID, result any, err error) {
	m := wire{JSONRPC: "2.0", ID: &id}
	if err != nil {
		var je *Error
		if !errors.As(err, &je) {
			je = &Error{Code: CodeInternalError, Message: err.Error()}
		}
		m.Error = je
	} else {
		b, merr := json.Marshal(result)
		if merr != nil {
			m.Error = &Error{Code: CodeInternalError, Message: merr.Error()}
		} else {
			m.Result = b
		}
	}
	if werr := c.write(&m); werr != nil {
		c.logf("reply to %s: %v", id, werr)
	}
}

// Notify sends a notification to the peer.
func (c *Conn) Notify(method string, params any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(&wire{JSONRPC: "2.0", Method: method, Params: b})
}

// Call sends a request to the peer and decodes its result into result,
// which may be nil. It returns the peer's *Error if it answers with one.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ch := make(chan *wire, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return errors.New("jsonrpc: connection closed")
	default:
	}
	c.nextID++
	id := IntID(c.nextID)
	c.pending[id.raw] = ch
	c.mu.Unlock()
	if err := c.write(&wire{JSONRPC: "2.0", ID: &id, Method: method, Params: b}); err != nil {
		c.mu.Lock()
		delete(c.pending, id.raw)
		c.mu.Unlock()
		return err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return errors.New("jsonrpc: connection closed")
		}
		if m.Error != nil {
			return m.Error
		}
		if result != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id.raw)
		c.mu.Unlock()
		_ = c.Notify("$/cancelRequest", map[string]any{"id": id})
		return ctx.Err()
	}
}

func (c *Conn) write(m *wire) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n", len(b)); err != nil {
		return err
	}
	_, err = c.w.Write(b)
	return err
}

// maxFrame bounds a message's size, so that a corrupt header can't make
// the reader allocate without limit.
const maxFrame = 256 << 20

// readFrame reads one message's body.
func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF && line == "" && length < 0 {
				return nil, io.EOF
			}
			if err == io.EOF {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if length < 0 {
				// Blank lines between messages are tolerated.
				continue
			}
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("jsonrpc: invalid header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 0 || n > maxFrame {
				return nil, fmt.Errorf("jsonrpc: invalid Content-Length %q", value)
			}
			length = n
		}
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return body, nil
}
