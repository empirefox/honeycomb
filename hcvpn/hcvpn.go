package hcvpn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/empirefox/cement/clog"
	"github.com/empirefox/honeycomb/hctox"
	"github.com/songgao/water"
	"github.com/vishvananda/netlink"

	"go.uber.org/zap"
)

type Peer struct {
	Label  string
	ToxID  string   `validate:"len=76"`
	Secret string   `validate:"gte=16"`
	Port   uint16   `validate:"required"`
	LanIP  string   `validate:"ipv4"`
	Route  []string `validate:"dive,cidrv4"`
}

type Config struct {
	TUNName   string
	Broadcast string
	NetCIDR   uint16 `validate:"gte=8,lte=30"`
	Tox       hctox.Config
	Peers     []Peer
}

func (c *Config) Self(toxid string) (*Peer, bool) {
	for _, p := range c.Peers {
		if p.ToxID == toxid {
			return &p, true
		}
	}
	return nil, false
}

type ip2pc map[[4]byte]*PeerConn
type tox2pc map[uint32]*PeerConn

type Vpn struct {
	log     *zap.Logger
	config  *Config
	ctx     context.Context
	tox     *hctox.Tox
	self    *Peer
	pubkey  string
	cidr    string // 192.168.3.6/24
	iface   *water.Interface
	link    netlink.Link
	bcastIP [4]byte

	// ip2pc connect with remote
	// target ip => Peerconn
	ip2pc ip2pc

	pubkey2pc map[string]*PeerConn
	tox2pc    atomic.Value
	tox2pcMu  sync.Mutex
}

func NewVpn(ctx context.Context, config Config, cl clog.Logger) (*Vpn, error) {
	l := cl.Module("VPN")

	v := &Vpn{
		log:    l,
		config: &config,
		ctx:    ctx,
	}
	v.tox2pc.Store(make(tox2pc))

	bIP := net.ParseIP(config.Broadcast)
	if bIP != nil {
		v.bcastIP = [4]byte{bIP[12], bIP[13], bIP[14], bIP[15]}
	}

	config.Tox.Peers = make(map[string]string)
	config.Tox.Supers = make(map[string]string)
	for _, p := range config.Peers {
		config.Tox.Peers[p.ToxID] = p.Secret
	}

	t, err := hctox.NewTox(config.Tox, v, cl)
	if err != nil {
		return nil, err
	}
	v.tox = t

	self, ok := config.Self(t.ID)
	if !ok {
		l.Warn("Self toxid not listed in peer.conf")
		return nil, errors.New("Self toxid not listed in peers")
	}
	v.self = self
	v.pubkey = string(self.ToxID[:64])
	v.cidr = fmt.Sprintf("%s/%d", self.LanIP, config.NetCIDR)

	iface, link, err := v.ifaceSetup()
	if err != nil {
		return nil, err
	}
	v.iface = iface
	v.link = link

	ps := make(ip2pc)
	pubkey2pc := make(map[string]*PeerConn)
	for _, p := range config.Peers {
		if p.ToxID != t.ID {
			pc, err := NewPeerConn(v, &p, t.Send, cl)
			if err != nil {
				return nil, err
			}
			ps[pc.IP] = pc
			pubkey2pc[pc.PubKey] = pc
		}
	}
	v.ip2pc = ps
	v.pubkey2pc = pubkey2pc

	return v, nil
}

func (v *Vpn) Run() {
	go v.ifaceReading()
	v.tox.Run()
}

func (v *Vpn) Stop() {
	v.iface.Close()
	for _, pc := range v.ip2pc {
		pc.Close()
	}
	<-v.tox.StopAndKill()
}

func (v *Vpn) getOrCreatePeer(friendId string, friendNumber uint32) *PeerConn {
	pc, ok := v.tox2pc.Load().(tox2pc)[friendNumber]
	if !ok {
		pc, ok = v.pubkey2pc[friendId]
		if !ok {
			v.log.Warn("peer not found", zap.String("friendId", friendId))
			return nil
		}
		pc.StartOnce(v.ctx, friendNumber, strings.Compare(v.pubkey, friendId) > 0)

		v.tox2pcMu.Lock()
		defer v.tox2pcMu.Unlock()
		m1 := v.tox2pc.Load().(tox2pc)
		m2 := make(tox2pc)
		for k, v := range m1 {
			m2[k] = v
		}
		m2[friendNumber] = pc
		v.tox2pc.Store(m2)
	}
	return pc
}

// Tox Callback start

func (v *Vpn) OnSelfConnectionStatus(status int) {
	v.log.Debug("Vpn online")
}

func (v *Vpn) OnPeerMessage(friendId string, friendNumber uint32, message []byte) {
	p := v.getOrCreatePeer(friendId, friendNumber)
	if p != nil {
		p.OnSignalingMessage(message)
	} else {
		v.log.Debug("Peer not found", zap.String("friendId", friendId))
	}
}

func (v *Vpn) OnSuperMessage(friendId string, friendNumber uint32, message []byte) {
	v.log.Warn("Super not implemented", zap.String("friendId", friendId))
}

func (v *Vpn) OnPeerOnline(friendId string, friendNumber uint32) {
	v.getOrCreatePeer(friendId, friendNumber)
	v.log.Debug("peer online", zap.String("friendId", friendId))
}

// Tox Callback end
