package main

import (
	"net"
	"testing"
	"time"
)

func acceptAsync(l net.Listener) <-chan net.Conn {
	ch := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			close(ch)
			return
		}
		ch <- c
	}()
	return ch
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestLimitListener_BlocksAtCapUntilAConnectionCloses(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := newLimitListener(base, 1)
	defer l.Close()
	addr := base.Addr().String()

	dial(t, addr)
	first, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}

	dial(t, addr)
	second := acceptAsync(l)
	select {
	case <-second:
		t.Fatal("second connection accepted while the cap was reached")
	case <-time.After(200 * time.Millisecond):
	}

	// Closing twice must free one slot, not two.
	first.Close()
	first.Close()
	select {
	case c := <-second:
		if c == nil {
			t.Fatal("accept failed after a slot was freed")
		}
		dial(t, addr)
		third := acceptAsync(l)
		select {
		case <-third:
			t.Fatal("a double Close freed two slots")
		case <-time.After(200 * time.Millisecond):
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second connection was not accepted after the first closed")
	}
}

func TestLimitListener_CloseUnblocksAWaitingAccept(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := newLimitListener(base, 1)
	dial(t, base.Addr().String())
	if _, err := l.Accept(); err != nil {
		t.Fatal(err)
	}

	waiting := acceptAsync(l)
	time.Sleep(100 * time.Millisecond)
	l.Close()
	select {
	case c, ok := <-waiting:
		if ok && c != nil {
			t.Fatal("Accept returned a connection after Close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Accept stayed blocked after Close")
	}
}
