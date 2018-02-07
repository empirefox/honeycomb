package hcrtc

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
)

var (
	ErrNoConnector = errors.New("No rtc connector")
	ErrClosed      = errors.New("already closed")
)

type MergeListener struct {
	errOnEmpty atomic.Value
	onfailed   func(r *RtcConnector)
	resultCh   chan net.Conn
	done       chan struct{}
	closeonce  sync.Once
	closed     atomic.Value
	n          int32
}

func NewMergeListener(onfailed func(r *RtcConnector)) *MergeListener {
	ml := &MergeListener{
		onfailed: onfailed,
		resultCh: make(chan net.Conn, 16),
		done:     make(chan struct{}),
	}
	ml.errOnEmpty.Store(false)
	return ml
}

func (ml MergeListener) ErrEmpty(e bool) {
	ml.errOnEmpty.Store(e)
}

func (ml MergeListener) Add(r *RtcConnector) {
	atomic.AddInt32(&ml.n, 1)
	go func() {
		defer atomic.AddInt32(&ml.n, -1)
		for {
			select {
			case dc := <-r.dcs:
				ml.resultCh <- NewConn(dc, r.Local, r.Remote, r.log)
			case <-r.pcfailed:
				if ml.onfailed != nil {
					ml.onfailed(r)
				}
				return
			case <-ml.done:
			}
		}
	}()
}

func (ml MergeListener) Accept() (net.Conn, error) {
	if ml.errOnEmpty.Load().(bool) && atomic.LoadInt32(&ml.n) == 0 {
		return nil, ErrNoConnector
	}
	select {
	case c := <-ml.resultCh:
		return c, nil
	case <-ml.done:
		return nil, ErrClosed
	}
}

func (ml MergeListener) Close() error {
	ml.closeonce.Do(func() { close(ml.done) })
	return nil
}

func (ml MergeListener) Addr() net.Addr {
	return newAddr("merger")
}
