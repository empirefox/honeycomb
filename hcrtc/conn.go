package hcrtc

import (
	"errors"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/keroserene/go-webrtc"
)

var (
	ErrChannelClose = errors.New("channel.ReadyState close/closing")
	ErrReadTimeout  = errors.New("Read timeout")
)

type Conn struct {
	*webrtc.DataChannel
	log          *zap.Logger
	localAddr    *addr
	remoteAddr   *addr
	recvQueue    chan []byte
	readDeadline time.Time
	recvTmp      []byte
}

func NewConn(dc *DataChannel, local, remote string, log *zap.Logger) *Conn {
	c := &Conn{
		DataChannel: dc.DataChannel,
		log:         log,
		localAddr:   newAddr(local),
		remoteAddr:  newAddr(remote),
		recvQueue:   make(chan []byte, 64),
	}

	var once sync.Once

	opened := make(chan struct{})
	// not fired when OnDataChannel
	c.OnOpen = func() {
		c.log.Debug("channel.OnOpen", zap.String("remote", remote))
		once.Do(func() { close(opened) })
	}

	// TODO not fired when remote disconnect
	c.OnClose = func() {
		c.log.Debug("channel.OnClose", zap.String("remote", remote))
	}

	c.OnMessage = func(b []byte) {
		c.recvQueue <- b
	}

	dc.Done()
	<-opened
	return c
}

func (c *Conn) Read(b []byte) (n int, err error) {
	size := len(b)

	if c.recvTmp != nil {
		lm := len(c.recvTmp)
		if lm > size {
			copy(b, c.recvTmp[:size])
			c.recvTmp = c.recvTmp[size:]
			return size, nil
		} else {
			copy(b, c.recvTmp)
			c.recvTmp = nil
			return lm, nil
		}
	}

	timeout := make(chan struct{})
	if !c.readDeadline.IsZero() {
		c.readDeadline = time.Time{}
		go func() {
			time.Sleep(c.readDeadline.Sub(time.Now()))
			close(timeout)
		}()
	}

	select {
	case m, ok := <-c.recvQueue:
		if !ok {
			return 0, ErrChannelClose
		}
		lm := len(m)
		if lm > size {
			copy(b, m[:size])
			c.recvTmp = m[size:]
			return size, nil
		} else {
			copy(b, m)
			return lm, nil
		}
	case <-timeout:
		return 0, ErrReadTimeout
	}
}

func (c *Conn) Write(b []byte) (n int, err error) {
	if c.ReadyState() != webrtc.DataStateOpen {
		return 0, ErrChannelClose
	}
	c.Send(b)
	return len(b), nil
}

func (c *Conn) LocalAddr() net.Addr  { return c.localAddr }
func (c *Conn) RemoteAddr() net.Addr { return c.remoteAddr }
func (c *Conn) SetDeadline(t time.Time) error {
	c.readDeadline = t
	return nil
}
func (c *Conn) SetReadDeadline(t time.Time) error {
	c.readDeadline = t
	return nil
}
func (c *Conn) SetWriteDeadline(t time.Time) error { return nil }

type addr struct {
	label string
}

func newAddr(label string) *addr { return &addr{label} }
func (a *addr) Network() string  { return "rtc" }
func (a *addr) String() string   { return a.label }
