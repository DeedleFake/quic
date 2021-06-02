package fquic

import (
	"context"
	"crypto/tls"
	"net"
	"sync"

	"github.com/lucas-clemente/quic-go"
	"golang.org/x/sync/errgroup"
)

type Conn struct {
	session quic.Session

	streams    chan *Stream
	streamErr  error
	streamLock sync.RWMutex

	closer sync.Once
	done   chan struct{}
}

func newConn(session quic.Session) *Conn {
	c := Conn{
		session: session,
		streams: make(chan *Stream),
		done:    make(chan struct{}),
	}
	go c.acceptStreams()
	return &c
}

func Dial(address string) (*Conn, error) {
	return new(Dialer).Dial(address)
}

func Client(conn net.PacketConn, raddr net.Addr, host string) (*Conn, error) {
	return new(Dialer).Client(conn, raddr, host)
}

func (c *Conn) acceptStreams() {
	defer close(c.streams)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-c.done
		cancel()
	}()

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		for {
			s, err := c.session.AcceptStream(ctx)
			if err != nil {
				return err
			}

			c.streams <- newStream(c, s, s)
		}
	})

	eg.Go(func() error {
		for {
			s, err := c.session.AcceptUniStream(ctx)
			if err != nil {
				return err
			}

			c.streams <- newStream(c, nil, s)
		}
	})

	err := eg.Wait()
	if err != nil {
		c.streamLock.Lock()
		defer c.streamLock.Unlock()

		c.streamErr = err
	}
}

// Close closes the connection with a zero error code and a blank
// description.
func (c *Conn) Close() error {
	return c.CloseWithError(0, "")
}

func (c *Conn) CloseWithError(code uint64, desc string) error {
	c.closer.Do(func() {
		close(c.done)
	})
	return c.session.CloseWithError(quic.ApplicationErrorCode(code), desc)
}

func (c *Conn) AcceptStream(ctx context.Context) (*Stream, error) {
	// TODO: Make sure that this returns the correct errors in different
	// types of situations.

	select {
	case <-ctx.Done():
		return nil, ctx.Err()

	case s, ok := <-c.streams:
		if !ok {
			c.streamLock.RLock()
			defer c.streamLock.RUnlock()

			return nil, c.streamErr
		}
		return s, nil
	}
}

func (c *Conn) NewStream(unidirectional bool) (*Stream, error) {
	if unidirectional {
		s, err := c.session.OpenUniStream()
		if err != nil {
			return nil, err
		}
		return newStream(c, s, nil), nil
	}

	s, err := c.session.OpenStream()
	if err != nil {
		return nil, err
	}
	return newStream(c, s, s), nil
}

func (c *Conn) LocalAddr() net.Addr {
	return c.session.LocalAddr()
}

func (c *Conn) RemoteAddr() net.Addr {
	return c.session.RemoteAddr()
}

func (c *Conn) Session() quic.Session {
	return c.session
}

type Dialer struct {
	// TLSConfig is the TLS configuration to use when dialing a new
	// connection. If it is a nil, a sane default configuration is used.
	TLSConfig *tls.Config

	// QuicConfig is the quic-go configuration to use when dialing a new
	// connection. If it is nil, a sane default configuration is used.
	QuicConfig *quic.Config

	// Protocol, if non-empty, is used to build the NextProtos
	// specification of TLSConfig. One or the other must be specified.
	// If neither are specified, dialing operations will panic. If both
	// are specified, Protocol will be prepended to the list specified
	// in NextProtos.
	Protocol string
}

func (d *Dialer) tlsConfig() *tls.Config {
	conf := d.TLSConfig.Clone()
	if conf == nil {
		conf = new(tls.Config)
	}

	if d.Protocol != "" {
		conf.NextProtos = append([]string{d.Protocol}, conf.NextProtos...)
	}

	if len(conf.NextProtos) == 0 {
		panic("no protocol specified")
	}

	return conf
}

func (d *Dialer) Dial(address string) (*Conn, error) {
	return d.DialContext(context.Background(), address)
}

func (d *Dialer) DialContext(ctx context.Context, address string) (*Conn, error) {
	session, err := quic.DialAddrContext(ctx, address, d.tlsConfig(), d.QuicConfig)
	if err != nil {
		return nil, err
	}

	return newConn(session), nil
}

func (d *Dialer) Client(conn net.PacketConn, raddr net.Addr, host string) (*Conn, error) {
	return d.ClientContext(context.Background(), conn, raddr, host)
}

func (d *Dialer) ClientContext(ctx context.Context, conn net.PacketConn, raddr net.Addr, host string) (*Conn, error) {
	session, err := quic.DialContext(ctx, conn, raddr, host, d.tlsConfig(), d.QuicConfig)
	if err != nil {
		return nil, err
	}

	return newConn(session), nil
}
