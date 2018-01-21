package hcpeer

import (
	"net"
	"sync/atomic"

	"github.com/empirefox/cement/clog"
	"github.com/empirefox/honeycomb/hcrtc"
	"github.com/empirefox/honeycomb/hctox"
	"github.com/keroserene/go-webrtc"

	"go.uber.org/zap"
)

type PkPeerConnMap map[string]*PeerConn
type PeerConnMap map[uint32]*PeerConn

type PeerConn struct {
	Added     atomic.Value
	Online    atomic.Value
	Signaling *PeerSignaling
	Connector *hcrtc.RtcConnector
}

type Conductor struct {
	cl  clog.Logger
	log *zap.Logger
	tox *hctox.Tox

	activepk2pc  PkPeerConnMap
	passivepk2pc PkPeerConnMap
	name2pc      PkPeerConnMap

	active2pc  atomic.Value
	passive2pc atomic.Value
}

func NewConductor(config hctox.Config, cl clog.Logger) (*Conductor, error) {
	l := cl.Module("hcpeer")

	v := &Conductor{
		cl:  cl,
		log: l,

		activepk2pc:  make(PkPeerConnMap),
		passivepk2pc: make(PkPeerConnMap),
		name2pc:      make(PkPeerConnMap),
	}

	t, err := hctox.NewTox(config, v, cl)
	if err != nil {
		return nil, err
	}
	v.tox = t

	for _, p := range config.Actives {
		if p.ToxID != t.ID {
			pubkey := string(p.ToxID[:64])
			signaling := NewSignaling(t, pubkey)
			connector, err := hcrtc.NewConnector(signaling, p.Name, cl)
			if err != nil {
				return nil, err
			}
			pc := &PeerConn{
				Signaling: signaling,
				Connector: connector,
			}
			pc.Added.Store(false)
			pc.Online.Store(false)
			v.activepk2pc[pubkey] = pc
			v.name2pc[p.Name] = pc
		}
	}
	for _, p := range config.Passives {
		if p.ToxID != t.ID {
			pubkey := string(p.ToxID[:64])
			signaling := NewSignaling(t, pubkey)
			connector, err := hcrtc.NewConnector(signaling, p.Name, cl)
			if err != nil {
				return nil, err
			}
			pc := &PeerConn{
				Signaling: signaling,
				Connector: connector,
			}
			pc.Added.Store(false)
			pc.Online.Store(false)
			v.passivepk2pc[pubkey] = pc
			v.name2pc[p.Name] = pc
		}
	}

	v.active2pc.Store(make(PeerConnMap))
	v.passive2pc.Store(make(PeerConnMap))

	return v, nil
}

func (v *Conductor) ToxUsable(name string) (bool, error) {
	if pc, ok := v.name2pc[name]; ok {
		return pc.Signaling.IsUsable(), nil
	}
	return false, &AcceptError{name}
}

func (v *Conductor) AcceptChannel(name string) (*webrtc.DataChannel, error) {
	if pc, ok := v.name2pc[name]; ok {
		return pc.Connector.AcceptChannel()
	}
	return nil, &AcceptError{name}
}

func (v *Conductor) CreateChannel(name string) (*webrtc.DataChannel, error) {
	if pc, ok := v.name2pc[name]; ok {
		return pc.Connector.CreateChannel()
	}
	return nil, &AcceptError{name}
}

func (v *Conductor) Accept(name string) (net.Conn, error) {
	if pc, ok := v.name2pc[name]; ok {
		return pc.Connector.Accept()
	}
	return nil, &AcceptError{name}
}

func (v *Conductor) Dial(name string) (net.Conn, error) {
	if pc, ok := v.name2pc[name]; ok {
		return pc.Connector.Dial()
	}
	return nil, &AcceptError{name}
}

func (v *Conductor) Run() {
	v.tox.Run()
}

func (v *Conductor) Stop() {
	for _, pc := range v.name2pc {
		pc.Connector.Close()
	}
	v.tox.Stop()
}

func (v *Conductor) FindOnlineConnector(toxid string) (*hcrtc.RtcConnector, bool) {
	pc, ok := v.activepk2pc[toxid]
	if !ok {
		pc, ok = v.passivepk2pc[toxid]
	}
	if pc.Online.Load().(bool) {
		return pc.Connector, true
	}
	return nil, false
}

func (v *Conductor) FindOnlineConnectorByName(name string) (*hcrtc.RtcConnector, bool) {
	pc, ok := v.name2pc[name]
	if ok && pc.Online.Load().(bool) {
		return pc.Connector, true
	}
	return nil, false
}

// Tox Callback start

func (v *Conductor) OnSelfConnectionStatus(status int) {
	v.log.Debug("Tox self status", zap.Int("status", status))
	// TODO offline all peers?
}

func (v *Conductor) OnActiveAdded(friendId string, friendNumber uint32) {
	pc, ok := v.activepk2pc[friendId]
	if !ok {
		v.log.Error("Active Peer not found", zap.String("friendId", friendId))
		return
	}

	m1 := v.active2pc.Load().(PeerConnMap)
	m2 := make(PeerConnMap)
	for k, v := range m1 {
		m2[k] = v // copy all data from the current object to the new one
	}
	m2[friendNumber] = pc // do the update that we need
	v.active2pc.Store(m2) // atomically replace the current object with the new one
	pc.Added.Store(true)
}

func (v *Conductor) OnPassiveAdded(friendId string, friendNumber uint32) {
	pc, ok := v.passivepk2pc[friendId]
	if !ok {
		v.log.Error("Passive Peer not found", zap.String("friendId", friendId))
		return
	}

	m1 := v.passive2pc.Load().(PeerConnMap)
	m2 := make(PeerConnMap)
	for k, v := range m1 {
		m2[k] = v // copy all data from the current object to the new one
	}
	m2[friendNumber] = pc  // do the update that we need
	v.passive2pc.Store(m2) // atomically replace the current object with the new one
	pc.Added.Store(true)
}

func (v *Conductor) OnToxMessage(friendNumber uint32, message []byte) {
	pc, ok := v.active2pc.Load().(PeerConnMap)[friendNumber]
	if !ok {
		pc, ok = v.passive2pc.Load().(PeerConnMap)[friendNumber]
	}
	if !ok {
		return
	}
	pc.Connector.HandleSignalingMessage(message)
}

func (v *Conductor) OnToxOnline(friendNumber uint32, on bool) {
	pc, ok := v.active2pc.Load().(PeerConnMap)[friendNumber]
	if !ok {
		pc, ok = v.passive2pc.Load().(PeerConnMap)[friendNumber]
	}
	if !ok {
		return
	}

	pc.Online.Store(on)
	pc.Signaling.Usable(on, friendNumber)
	v.log.Debug("Peer online", zap.Uint32("friendNumber", friendNumber), zap.Bool("on", on))
}

// Tox Callback end
