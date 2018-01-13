package hcvpn

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/empirefox/cement/clog"
	"github.com/empirefox/honeycomb/hcrtc"
	"github.com/keroserene/go-webrtc"
	"github.com/vishvananda/netlink"
	"go.uber.org/zap"
)

const (
	// I use TUN interface, so only plain IP packet,
	// no ethernet header + mtu is set to 1300

	// BUFFERSIZE is size of buffer to receive packets
	// (little bit bigger than maximum)
	BUFFERSIZE = 1518
)

var ErrPeerClosed = errors.New("Peer closed")

type PeerConn struct {
	Peer
	v   *Vpn
	cl  clog.Logger
	log *zap.Logger

	IP     [4]byte
	routes []*netlink.Route

	ID     uint32
	PubKey string

	onSignalingMessage func(msg []byte)
	signalingMessage   func(id uint32, msg []byte) error
	connector          *hcrtc.RtcConnector

	in   chan []byte
	out  chan []byte
	done chan struct{}

	wg        sync.WaitGroup
	runOnce   sync.Once
	closeOnce sync.Once
}

func NewPeerConn(v *Vpn, p *Peer, signalingMessage func(id uint32, msg []byte) error, cl clog.Logger) (*PeerConn, error) {
	if p.ToxID == v.self.ToxID {
		v.log.Warn("Try to add self to peer")
		return nil, errors.New("Try to add self to peer")
	}

	tIP := net.ParseIP(p.LanIP)
	if tIP == nil {
		v.log.Warn("ParseIP", zap.String("LanIP", p.LanIP), zap.String("Label", p.Label))
		return nil, errors.New("Bad LanIP")
	}

	var routes []*netlink.Route
	for _, routestr := range p.Route {
		_, dst, err := net.ParseCIDR(routestr)
		if nil != err {
			v.log.Warn("ParseCIDR", zap.Error(err), zap.String("Route", routestr), zap.String("Label", p.Label))
			return nil, err
		}
		route := &netlink.Route{
			LinkIndex: v.link.Attrs().Index,
			Scope:     netlink.SCOPE_UNIVERSE,
			Dst:       dst,
		}
		routes = append(routes, route)
	}

	pc := &PeerConn{
		Peer: *p,
		v:    v,
		cl:   cl,
		log:  v.log,

		IP:     [4]byte{tIP[12], tIP[13], tIP[14], tIP[15]},
		routes: routes,

		PubKey:           string(p.ToxID[:64]),
		signalingMessage: signalingMessage,

		in:   make(chan []byte, 100),
		out:  make(chan []byte, 100),
		done: make(chan struct{}),
	}

	connector, err := hcrtc.NewConnector(pc, pc.cl)
	if err != nil {
		return nil, err
	}
	pc.connector = connector

	return pc, nil
}

func (pc *PeerConn) Write(b []byte) {
	select {
	case pc.out <- b:
	case <-time.After(time.Millisecond * 100):
	default:
	}
}

func (pc *PeerConn) Close() {
	pc.closeOnce.Do(pc.close)
}

func (pc *PeerConn) StartOnce(ctx context.Context, id uint32, starter bool) {
	pc.runOnce.Do(func() {
		pc.wg.Add(1)
		go pc.runonce(ctx, id, starter)
	})
}

func (pc *PeerConn) close() {
	close(pc.done)
	for _, route := range pc.routes {
		err := netlink.RouteDel(route)
		pc.log.Info("RouteDel", zap.Error(err), zap.Stringer("net", route.Dst))
	}
	pc.wg.Wait()
	pc.connector.Close()
}

func (pc *PeerConn) runonce(ctx context.Context, id uint32, starter bool) {
	defer pc.wg.Done()

	for _, route := range pc.routes {
		err := netlink.RouteAdd(route)
		pc.log.Info("RouteAdd", zap.Error(err), zap.Stringer("net", route.Dst))
	}

	pc.ID = id

	pc.log.Debug("PeerConn starting", zap.Bool("starter", starter))
	if starter {
		pc.connectorrouting(ctx)
	}
}

func (pc *PeerConn) connectorrouting(ctx context.Context) {
	var tempDelay time.Duration
	create := make(chan struct{})
	go func() { create <- struct{}{} }()
	for {
		select {
		case <-create:
			channel, err := pc.connector.CreateChannel()
			if err != nil {
				if tempDelay == 0 {
					tempDelay = 1 * time.Second
				} else {
					tempDelay *= 2
				}
				if max := 16 * time.Second; tempDelay > max {
					tempDelay = max
				}
				pc.log.Debug("CreateChannel", zap.Error(err), zap.Duration("tempDelay", tempDelay))
				go func() {
					time.Sleep(tempDelay)
					create <- struct{}{}
				}()
			} else {
				pc.wg.Add(1)
				go pc.channelrouting(ctx, channel, create)
			}

		case <-pc.done:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (pc *PeerConn) channelrouting(ctx context.Context, channel *webrtc.DataChannel, closed chan<- struct{}) {
	defer func() {
		if closed != nil {
			closed <- struct{}{}
		}
		channel.Close()
		pc.wg.Done()
	}()

	done := make(chan struct{})
	channel.OnOpen = func() {
		pc.log.Debug("channel.OnOpen")
	}
	channel.OnClose = func() {
		close(done)
		pc.log.Debug("channel.OnClose")
	}
	channel.OnMessage = func(b []byte) {
		//					pc.in <- b
		n, err := pc.v.iface.Write(b)
		if err != nil {
			pc.log.Warn("iface.Write", zap.Error(err))
		} else if n != MTU {
			pc.log.Warn("iface.Write Partial package written to local interface")
		}
	}

	var sendQueue [][]byte
	for {
		select {
		case b := <-pc.out:
			switch channel.ReadyState() {
			case webrtc.DataStateConnecting:
				sendQueue = append(sendQueue, b)

			case webrtc.DataStateOpen:
				if len(sendQueue) != 0 {
					for _, msg := range sendQueue {
						channel.Send(msg)
					}
					sendQueue = nil
				}
				channel.Send(b)

			default:
				pc.log.Debug("channel.ReadyState close/closing")
				return
			}

		case <-done:
			return
		case <-pc.done:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (pc *PeerConn) OnSignalingMessage(msg []byte) {
	pc.onSignalingMessage(msg)
}

// hcrtc.Callback start

func (pc *PeerConn) SignalingSend(msg []byte) error {
	return pc.signalingMessage(pc.ID, msg)
}

func (pc *PeerConn) OnSignalingMessageFunc(fn func(msg []byte)) {
	pc.onSignalingMessage = fn
}

func (pc *PeerConn) OnIncome(channel *webrtc.DataChannel) {
	pc.wg.Add(1)
	go pc.channelrouting(context.Background(), channel, nil)
}

// hcrtc.Callback end
