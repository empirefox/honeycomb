package hcpeer

import (
	"errors"
	"sync"

	"github.com/empirefox/cement/clog"
	"github.com/empirefox/honeycomb/hctox"
	"github.com/mcuadros/go-defaults"
	"go.uber.org/zap"
)

type Config struct {
	PeersFile  string `default:"$HC_HOME/peers.conf"`
	SupersFile string `default:"$HC_HOME/supers.conf"`
}

// Controller bridge between Tox and Rtc
type Controller struct {
	cl         clog.Logger
	log        *zap.Logger
	config     *Config
	tox        *hctox.Tox
	self       *PeerInfo
	peers      map[uint32]*Peer
	infoPeers  map[string]PeerInfo
	infoSupers map[string]PeerInfo
	peerMu     sync.Mutex
}

func NewController(config Config, cl clog.Logger) (*Controller, error) {
	l := cl.Module("PEER")
	defaults.SetDefaults(&config)

	c := Controller{
		cl:         cl,
		log:        l,
		config:     &config,
		peers:      make(map[uint32]*Peer),
		infoPeers:  make(map[string]PeerInfo),
		infoSupers: make(map[string]PeerInfo),
	}

	toxConfig := hctox.Config{
		Peers:  make(map[string]string),
		Supers: make(map[string]string),
	}

	infoPeers := ReadPeers(config.PeersFile, l)
	for _, info := range infoPeers {
		c.infoPeers[info.PubKey] = info
		toxConfig.Peers[info.ToxID] = info.Secret
	}

	infoSupers := ReadSupers(config.SupersFile, l)
	for _, info := range infoSupers {
		c.infoSupers[info.PubKey] = info
		toxConfig.Supers[info.ToxID] = info.Secret
	}

	t, err := hctox.NewTox(toxConfig, &c, cl)
	if err != nil {
		return nil, err
	}

	self, ok := c.infoPeers[string(t.ID[:64])]
	if !ok {
		l.Warn("Self toxid not listed in peer.conf")
		return nil, errors.New("Self toxid not listed in peer.conf")
	}
	delete(c.infoPeers, self.PubKey)

	c.self = &self
	c.tox = t
	return &c, nil
}

func (c *Controller) Run() {
	c.tox.Run()
}

func (c *Controller) StopAndKill() {
	c.peerMu.Lock()
	defer c.peerMu.Unlock()

	c.tox.StopAndKill()
	for _, p := range c.peers {
		p.StopAndKill()
	}
}

func (c *Controller) getOrCreatePeer(super bool, friendId string, friendNumber uint32) *Peer {
	c.peerMu.Lock()
	defer c.peerMu.Unlock()

	p, ok := c.peers[friendNumber]
	if !ok {
		var infos map[string]PeerInfo
		if super {
			infos = c.infoSupers
		} else {
			infos = c.infoPeers
		}
		info, ok := infos[friendId]
		if !ok {
			c.log.Warn("peer not found", zap.String("friendId", friendId))
			return nil
		}
		var err error
		p, err = NewPeer(friendNumber, c.self, info, c.tox.Send, c.cl)
		if err != nil {
			return nil
		}
		c.peers[friendNumber] = p
		go p.Run()
	}
	return p
}

// Tox Callback start

func (c *Controller) OnSelfConnectionStatus(status int) {
	c.log.Debug("Controller online")
}

func (c *Controller) OnPeerMessage(friendId string, friendNumber uint32, message []byte) {
	p := c.getOrCreatePeer(false, friendId, friendNumber)
	if p != nil {
		p.OnSignalingMessage(message)
	} else {
		c.log.Debug("Peer not found", zap.String("friendId", friendId))
	}
}

func (c *Controller) OnSuperMessage(friendId string, friendNumber uint32, message []byte) {
	p := c.getOrCreatePeer(true, friendId, friendNumber)
	if p != nil {
		// TODO separate message
		p.OnSignalingMessage(message)
	} else {
		c.log.Debug("Peer not found", zap.String("friendId", friendId))
	}
}

func (c *Controller) OnPeerOnline(friendId string, friendNumber uint32) {
	c.getOrCreatePeer(false, friendId, friendNumber)
	c.log.Debug("peer online", zap.String("friendId", friendId))
}

// Tox Callback end
