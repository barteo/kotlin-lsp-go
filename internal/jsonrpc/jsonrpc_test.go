package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pair returns two connected Conns running until the test ends.
func pair(t *testing.T, a, b Handler) (*Conn, *Conn) {
	t.Helper()
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	ca := NewConn(ar, aw, a, t.Logf)
	cb := NewConn(br, bw, b, t.Logf)
	ctx, cancel := context.WithCancel(context.Background())
	go ca.Run(ctx)
	go cb.Run(ctx)
	t.Cleanup(func() {
		cancel()
		aw.Close()
		bw.Close()
		<-ca.Done()
		<-cb.Done()
	})
	return ca, cb
}

func none(context.Context, *Conn, *Request) (any, error) { return nil, ErrMethodNotFound }

func TestCall(t *testing.T) {
	_, client := pair(t, func(ctx context.Context, c *Conn, r *Request) (any, error) {
		switch r.Method {
		case "add":
			var p [2]int
			if err := json.Unmarshal(r.Params, &p); err != nil {
				return nil, Errorf(CodeInvalidParams, "%v", err)
			}
			return p[0] + p[1], nil
		case "boom":
			panic("boom")
		}
		return nil, ErrMethodNotFound
	}, none)
	var sum int
	if err := client.Call(context.Background(), "add", [2]int{2, 3}, &sum); err != nil || sum != 5 {
		t.Fatalf("add = %d, %v", sum, err)
	}
	err := client.Call(context.Background(), "nope", nil, nil)
	var je *Error
	if !errors.As(err, &je) || je.Code != CodeMethodNotFound {
		t.Fatalf("nope: %v", err)
	}
	err = client.Call(context.Background(), "boom", nil, nil)
	if !errors.As(err, &je) || je.Code != CodeInternalError {
		t.Fatalf("boom: %v", err)
	}
	// The server survives a panic.
	if err := client.Call(context.Background(), "add", [2]int{1, 1}, &sum); err != nil || sum != 2 {
		t.Fatalf("add after panic = %d, %v", sum, err)
	}
}

func TestNotificationsInOrder(t *testing.T) {
	var got []string
	done := make(chan struct{})
	_, client := pair(t, func(ctx context.Context, c *Conn, r *Request) (any, error) {
		switch r.Method {
		case "note":
			var s string
			json.Unmarshal(r.Params, &s)
			got = append(got, s)
			return nil, nil
		case "read":
			// A request sees every notification sent before it.
			return strings.Join(got, ","), nil
		}
		close(done)
		return nil, nil
	}, none)
	for _, s := range []string{"a", "b", "c"} {
		if err := client.Notify("note", s); err != nil {
			t.Fatal(err)
		}
	}
	var s string
	if err := client.Call(context.Background(), "read", nil, &s); err != nil || s != "a,b,c" {
		t.Fatalf("read = %q, %v", s, err)
	}
}

func TestCancel(t *testing.T) {
	started := make(chan struct{})
	_, client := pair(t, func(ctx context.Context, c *Conn, r *Request) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, none)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- client.Call(ctx, "slow", nil, nil) }()
	<-started
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Call = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled call didn't return")
	}
}

func TestCancelReply(t *testing.T) {
	started := make(chan struct{})
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	server := NewConn(inR, outW, func(ctx context.Context, c *Conn, r *Request) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, t.Logf)
	ctx, cancel := context.WithCancel(context.Background())
	go server.Run(ctx)
	t.Cleanup(func() {
		cancel()
		inW.Close()
		outR.Close()
		<-server.Done()
	})
	send := func(s string) {
		if _, err := io.WriteString(inW, "Content-Length: "+strconv.Itoa(len(s))+"\r\n\r\n"+s); err != nil {
			t.Fatal(err)
		}
	}
	send(`{"jsonrpc":"2.0","id":7,"method":"slow"}`)
	<-started
	send(`{"jsonrpc":"2.0","method":"$/cancelRequest","params":{"id":7}}`)
	b, err := readFrame(bufio.NewReader(outR))
	if err != nil {
		t.Fatal(err)
	}
	var m wire
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Error == nil || m.Error.Code != CodeRequestCancelled {
		t.Fatalf("reply = %s, want code %d", b, CodeRequestCancelled)
	}
}

func TestServerCallsClient(t *testing.T) {
	server, _ := pair(t, none, func(ctx context.Context, c *Conn, r *Request) (any, error) {
		if r.Method == "client/ping" {
			return "pong", nil
		}
		return nil, ErrMethodNotFound
	})
	var s string
	if err := server.Call(context.Background(), "client/ping", nil, &s); err != nil || s != "pong" {
		t.Fatalf("ping = %q, %v", s, err)
	}
}

func TestReadFrame(t *testing.T) {
	in := "Content-Length: 2\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n{}" +
		"\r\ncontent-length:3\r\n\r\n[1]"
	r := bufio.NewReader(strings.NewReader(in))
	for _, want := range []string{"{}", "[1]"} {
		b, err := readFrame(r)
		if err != nil || string(b) != want {
			t.Fatalf("readFrame = %q, %v; want %q", b, err, want)
		}
	}
	if _, err := readFrame(r); err != io.EOF {
		t.Fatalf("at end: %v, want EOF", err)
	}
	for _, bad := range []string{"Content-Length: x\r\n\r\n", "nonsense\r\n\r\n", "Content-Length: 5\r\n\r\n{}"} {
		if _, err := readFrame(bufio.NewReader(strings.NewReader(bad))); err == nil {
			t.Errorf("readFrame(%q) succeeded", bad)
		}
	}
}

func TestID(t *testing.T) {
	for _, in := range []string{`1`, `"abc"`, `-3`} {
		var id ID
		if err := json.Unmarshal([]byte(in), &id); err != nil || id.String() != in {
			t.Errorf("ID %s = %q, %v", in, id, err)
		}
		b, _ := json.Marshal(id)
		if string(b) != in {
			t.Errorf("marshal %s = %s", in, b)
		}
	}
	var id ID
	if err := json.Unmarshal([]byte(`{}`), &id); err == nil {
		t.Error("object accepted as id")
	}
}
