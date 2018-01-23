package hcvpn

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"github.com/empirefox/cement/clog"
	"github.com/empirefox/honeycomb/hcpeer"
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

	ip     [4]byte
	routes []*netlink.Route
	c      net.Conn
}

// Config for hcvpn
type Config struct {
	ToxSecretKey string `validate:"len=64"`
	ToxNospam    uint32 `validate:"required"`
	TUNName      string
	Broadcast    string
	NetCIDR      uint16 `validate:"gte=8,lte=30"`
	Peers        []Peer
}

type ip2pc map[[4]byte]*Peer

type Vpn struct {
	log       *zap.Logger
	config    *Config
	conductor *hcpeer.Conductor
	self      *Peer
	pubkey    string
	cidr      string // 192.168.3.6/24
	iface     *water.Interface
	link      netlink.Link
	bcastIP   [4]byte

	actives  []*Peer
	passives []*Peer

	// ip2pc connect with remote
	// target ip => Peerconn
	ip2pc  atomic.Value
	mu     sync.Mutex
	closed bool
}

func NewVpn(config Config, cl clog.Logger) (*Vpn, error) {
	l := cl.Module("VPN")

	toxid, err := hctox.ToxID(config.ToxSecretKey, config.ToxNospam)
	if err != nil {
		return nil, errors.New("Get self toxid failed")
	}

	v := &Vpn{
		log:    l,
		config: &config,
	}

	bIP := net.ParseIP(config.Broadcast)
	if bIP != nil {
		v.bcastIP = [4]byte{bIP[12], bIP[13], bIP[14], bIP[15]}
	}

	toxConfig := hctox.Config{
		ToxSecretKey: config.ToxSecretKey,
		ToxNospam:    config.ToxNospam,
	}

	for i := range config.Peers {
		peer := &config.Peers[i]

		// parse local ip of peer
		tIP := net.ParseIP(peer.LanIP)
		if tIP == nil {
			v.log.Error("ParseIP", zap.String("LanIP", peer.LanIP), zap.String("Label", peer.Label))
			return nil, errors.New("Bad LanIP")
		}
		peer.ip = [4]byte{tIP[12], tIP[13], tIP[14], tIP[15]}

		// parse route of peer
		var routes []*netlink.Route
		for _, routestr := range peer.Route {
			_, dst, err := net.ParseCIDR(routestr)
			if nil != err {
				v.log.Error("ParseCIDR", zap.Error(err), zap.String("Route", routestr), zap.String("Label", peer.Label))
				return nil, err
			}
			route := &netlink.Route{
				LinkIndex: v.link.Attrs().Index,
				Scope:     netlink.SCOPE_UNIVERSE,
				Dst:       dst,
			}
			routes = append(routes, route)
		}
		peer.routes = routes

		// group
		p := hctox.Peer{
			Name:   peer.Label,
			ToxID:  peer.ToxID,
			Secret: peer.Secret,
		}
		if p.ToxID > toxid {
			toxConfig.Actives = append(toxConfig.Actives, p)
			v.actives = append(v.actives, peer)
		} else if p.ToxID < toxid {
			toxConfig.Passives = append(toxConfig.Passives, p)
			v.passives = append(v.passives, peer)
		} else {
			v.self = peer
		}
	}
	if v.self == nil {
		return nil, errors.New("Self toxid not listed in peers")
	}

	conductor, err := hcpeer.NewConductor(toxConfig, cl)
	if err != nil {
		return nil, err
	}
	v.conductor = conductor
	go v.conductor.Run()

	v.pubkey = string(v.self.ToxID[:64])
	v.cidr = fmt.Sprintf("%s/%d", v.self.LanIP, config.NetCIDR)

	iface, link, err := v.ifaceSetup()
	if err != nil {
		return nil, err
	}
	v.iface = iface
	v.link = link

	v.ip2pc.Store(make(ip2pc))
	for _, p := range v.passives {
		go func(p *Peer) {
			// TODO try again?
			conn, err := conductor.Dial(p.Label)
			if err != nil {
				l.Error("Dial routing", zap.Error(err))
				return
			}
			v.addConn(p, conn)
		}(p)
	}
	for _, p := range v.actives {
		go func(p *Peer) {
			conn, err := conductor.Accept(p.Label)
			if err != nil {
				l.Error("Accept routing", zap.Error(err))
				return
			}
			v.addConn(p, conn)
		}(p)
	}

	return v, nil
}

func (v *Vpn) addConn(p *Peer, conn net.Conn) {
	v.mu.Lock() // synchronize with other potential writers
	defer v.mu.Unlock()
	if v.closed {
		conn.Close()
		return
	}
	p.c = conn
	go io.Copy(v.iface, conn)

	m1 := v.ip2pc.Load().(ip2pc) // load current value of the data structure
	m2 := make(ip2pc)            // create a new value
	for k, v := range m1 {
		m2[k] = v // copy all data from the current object to the new one
	}
	m2[p.ip] = p      // do the update that we need
	v.ip2pc.Store(m2) // atomically replace the current object with the new one

	for _, route := range p.routes {
		err := netlink.RouteAdd(route)
		v.log.Info("RouteAdd", zap.Error(err), zap.Stringer("net", route.Dst))
	}
}

func (v *Vpn) Close() {
	v.iface.Close()
	for i := range v.config.Peers {
		for _, route := range v.config.Peers[i].routes {
			err := netlink.RouteDel(route)
			v.log.Info("RouteDel", zap.Error(err), zap.Stringer("net", route.Dst))
		}
	}
	v.mu.Lock()
	v.closed = true
	pcs := v.ip2pc.Load().(ip2pc)
	v.ip2pc.Store(make(ip2pc))
	for _, pc := range pcs {
		pc.c.Close()
	}
	v.mu.Unlock()
}
