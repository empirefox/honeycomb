package hcpeer

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/empirefox/cement/clog"
	"github.com/empirefox/honeycomb/hcpipe"
	"github.com/empirefox/honeycomb/hcrtc"
	"github.com/keroserene/go-webrtc/data"
	"github.com/qiniu/log"
)

// 82:127.0.0.1:8282:6BA28AC06C1D57783FE017FA9322D0B356E61404C92155A04F64F3B19C75633E8BDDEFFA4856:SECRET
type Peer struct {
	PeerInfo

	ID   uint32
	self *PeerInfo

	onSignalingMessage func(msg []byte)
	SignalingMessage   func(id uint32, msg []byte) error

	log          *zap.Logger
	connector    *hcrtc.RtcConnector
	incomes      map[*data.Channel]net.Conn
	outgoings    map[*data.Channel]net.Conn
	closed       bool
	channelMutex sync.Mutex
	outCh        chan net.Conn
}

func NewPeer(id uint32, self *PeerInfo, info PeerInfo, signalingMessage func(id uint32, msg []byte) error, cl clog.Logger) (*Peer, error) {
	l := cl.Module("PEER")

	p := &Peer{
		PeerInfo:         info,
		ID:               id,
		self:             self,
		SignalingMessage: signalingMessage,
		log:              l,
		incomes:          make(map[*data.Channel]net.Conn),
		outgoings:        make(map[*data.Channel]net.Conn),
		outCh:            make(chan net.Conn),
	}

	connector, err := hcrtc.NewConnector(p, cl)
	if err != nil {
		return nil, err
	}

	p.connector = connector

	return p, nil
}

func (p *Peer) Run() error {
	l, err := net.Listen("tcp", fmt.Sprintf("%s:%s", p.HostOnRemote, p.PortOnRemote))
	if err != nil {
		p.log.Error("Peer Listen", zap.String("addr", fmt.Sprintf("%s:%s", p.HostOnRemote, p.PortOnRemote)))
		return err
	}
	defer l.Close()

	var tempDelay time.Duration // how long to sleep on accept failure
	for {
		c, e := l.Accept()
		if e != nil {
			if ne, ok := e.(net.Error); ok && ne.Temporary() {
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}
				if max := 1 * time.Second; tempDelay > max {
					tempDelay = max
				}
				p.log.Debug("http: Accept failed", zap.Error(e), zap.Duration("retrying", tempDelay))
				time.Sleep(tempDelay)
				continue
			}
			p.log.Error("Peer Run err", zap.Error(e))
			return e
		}
		tempDelay = 0
		go p.Pipe(context.Background(), c)
	}
}

func (p *Peer) Pipe(ctx context.Context, c net.Conn) error {
	channel, err := p.connector.CreateChannel()
	if err != nil {
		return err
	}

	if !p.Super {
		go func() {
			defer func() {
				p.delOutgoing(channel)
				c.Close()
				channel.Close()
			}()

			p.addOutgoing(channel, c)
			hcpipe.Pipe(ctx, c, channel, 64<<10, p.log)
		}()
	} else {
		// TODO send super command
		c.Close()
		channel.Close()
	}

	return nil
}

func (p *Peer) StopAndKill() {
	p.channelMutex.Lock()
	defer p.channelMutex.Unlock()

	for channel, c := range p.incomes {
		c.Close()
		channel.Close()
	}
	p.incomes = make(map[*data.Channel]net.Conn)

	for channel, c := range p.outgoings {
		c.Close()
		channel.Close()
	}
	p.outgoings = make(map[*data.Channel]net.Conn)

	p.connector.Close()
	p.closed = true
	p.log.Debug("Peer.StopAndKill")
}

func (p *Peer) OnSignalingMessage(msg []byte) {
	p.channelMutex.Lock()
	defer p.channelMutex.Unlock()

	if !p.closed {
		p.onSignalingMessage(msg)
	}
}

func (p *Peer) addIncome(channel *data.Channel, c net.Conn) {
	p.channelMutex.Lock()
	defer p.channelMutex.Unlock()
	p.incomes[channel] = c
}

func (p *Peer) delIncome(channel *data.Channel) {
	p.channelMutex.Lock()
	defer p.channelMutex.Unlock()
	delete(p.incomes, channel)
}

func (p *Peer) addOutgoing(channel *data.Channel, c net.Conn) {
	p.channelMutex.Lock()
	defer p.channelMutex.Unlock()
	p.outgoings[channel] = c
}

func (p *Peer) delOutgoing(channel *data.Channel) {
	p.channelMutex.Lock()
	defer p.channelMutex.Unlock()
	delete(p.outgoings, channel)
}

// hcrtc.Callback start

func (p *Peer) SignalingSend(msg []byte) error {
	return p.SignalingMessage(p.ID, msg)
}

func (p *Peer) OnSignalingMessageFunc(fn func(msg []byte)) {
	p.onSignalingMessage = fn
}

func (p *Peer) OnIncome(channel *data.Channel) {
	if !p.Super {
		defer func() {
			if err := recover(); err != nil {
				log.Error("OnIncome recover", zap.Any("err", err))
			}
		}()

		self := p.self
		c, err := net.Dial("tcp", fmt.Sprintf("%s:%s", self.HostOnRemote, self.PortOnRemote))
		if err != nil {
			p.log.Warn("income dail", zap.String("addr", fmt.Sprintf("%s:%s", self.HostOnRemote, self.PortOnRemote)), zap.Error(err))
			channel.Close()
			return
		}
		defer func() {
			p.delIncome(channel)
			c.Close()
			channel.Close()
		}()
		p.addIncome(channel, c)
		hcpipe.Pipe(context.Background(), c, channel, 64<<10, p.log)
	} else {
		// TODO parse super command
		channel.Close()
	}
}

// hcrtc.Callback end
