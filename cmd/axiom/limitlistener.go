package main

import (
	"net"
	"sync"
)

// limitListener caps how many accepted connections are open at once. When the
// cap is reached Accept waits for a slot instead of accepting, so excess
// clients queue in the kernel's listen backlog and are not served, rather than
// each one costing a goroutine and a handshake.
//
// It sits below TLS on purpose: a connection counts from accept, so peers
// stalled mid-handshake use up slots too, which is the case the cap exists for.
type limitListener struct {
	net.Listener
	slots     chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newLimitListener(l net.Listener, max int) *limitListener {
	return &limitListener{
		Listener: l,
		slots:    make(chan struct{}, max),
		done:     make(chan struct{}),
	}
}

func (l *limitListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &limitConn{Conn: c, release: func() { <-l.slots }}, nil
}

func (l *limitListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return l.Listener.Close()
}

type limitConn struct {
	net.Conn
	releaseOnce sync.Once
	release     func()
}

// Close frees the slot exactly once, however many times the server closes it.
func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.releaseOnce.Do(c.release)
	return err
}
