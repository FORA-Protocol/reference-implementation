package main

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
)

// tlsRecordHandshake is the first byte of every TLS connection: the record type
// of the ClientHello. No HTTP/1.x request line starts with it.
const tlsRecordHandshake = 0x16

// sniffTimeout bounds how long a new connection may take to send its first
// byte. Every client in the stack sends at once; this only drops one that never
// does.
const sniffTimeout = 10 * time.Second

// dualListener answers plain http and https on one port.
//
// A party in this stack is addressed by host:port, and the Web Bot Auth profile
// fetches its key directory over https at that same address: an Exchange whose
// identity is "exchange:8081" serves its directory at https://exchange:8081. So a
// port the proxy listens on has to speak both, and the first byte of each
// connection decides which. Each connection is classified on its own goroutine,
// so one slow client cannot hold up the next.
type dualListener struct {
	net.Listener
	tls   *tls.Config
	clk   clock.Clock
	ready chan net.Conn
	done  chan struct{}
	once  sync.Once
	err   error
}

// newDualListener starts classifying the connections ln accepts.
func newDualListener(ln net.Listener, cfg *tls.Config, clk clock.Clock) *dualListener {
	d := &dualListener{Listener: ln, tls: cfg, clk: clk, ready: make(chan net.Conn), done: make(chan struct{})}
	go d.acceptLoop()
	return d
}

func (d *dualListener) acceptLoop() {
	for {
		conn, err := d.Listener.Accept()
		if err != nil {
			d.stop(err)
			return
		}
		go d.classify(conn)
	}
}

// classify peeks at the first byte and hands the connection on as TLS or plain.
func (d *dualListener) classify(conn net.Conn) {
	_ = conn.SetReadDeadline(d.clk.Now().Add(sniffTimeout))
	reader := bufio.NewReader(conn)
	first, err := reader.Peek(1)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return
	}
	var classified net.Conn = &peekedConn{Conn: conn, reader: reader}
	if first[0] == tlsRecordHandshake {
		classified = tls.Server(classified, d.tls)
	}
	select {
	case d.ready <- classified:
	case <-d.done:
		_ = conn.Close()
	}
}

// Accept returns the next classified connection.
func (d *dualListener) Accept() (net.Conn, error) {
	select {
	case conn := <-d.ready:
		return conn, nil
	case <-d.done:
		return nil, d.err
	}
}

// Close stops accepting.
func (d *dualListener) Close() error {
	err := d.Listener.Close()
	d.stop(net.ErrClosed)
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// stop records why accepting ended, once, and wakes every waiter.
func (d *dualListener) stop(err error) {
	d.once.Do(func() {
		d.err = err
		close(d.done)
	})
}

// peekedConn reads the byte the classifier peeked at before the rest.
type peekedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (p *peekedConn) Read(b []byte) (int, error) { return p.reader.Read(b) }
