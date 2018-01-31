package hcpeer

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/empirefox/honeycomb/hcrtc"
	"github.com/empirefox/honeycomb/hctox"
	"github.com/kitech/go-toxcore"

	"go.uber.org/zap"
)

var (
	ErrAddSelf = errors.New("cannot add self")
)

type Server struct {
	ToxID string `validate:"len=76"`
	Token string `validate:"gte=16,lte=1015"`
}

type pkPeerConnMap map[string]*peerConn
type peerConnMap map[uint32]*peerConn

type peerConn struct {
	peer         Server
	friendNumber uint32
	online       atomic.Value
	signaling    *peerSignaling
	connector    *hcrtc.RtcConnector
}

type addServerCommand struct {
	pc *peerConn
	ch chan<- error
}

type Conductor struct {
	Log *zap.Logger

	Account *hctox.Account

	Validate func(friendId, msg string) bool

	OnConnector func(connector *hcrtc.RtcConnector)

	ReAddServerTimeout uint64

	tox    *tox.Tox
	selfpk string

	// pkPeerConnMap
	pk2pc atomic.Value

	// peerConnMap
	client2pc atomic.Value
	server2pc atomic.Value

	running atomic.Value

	addserver []*addServerCommand
	addpeerMu sync.Mutex
}

func (v *Conductor) Connector(pk string) (*hcrtc.RtcConnector, bool) {
	if pc, ok := v.pk2pc.Load().(pkPeerConnMap)[pk]; ok {
		return pc.connector, true
	}
	return nil, false
}

func (v *Conductor) Run(started chan struct{}) error {
	t, err := hctox.NewTox(v.Account, 0, false)
	if err != nil {
		return err
	}
	t.CallbackSelfConnectionStatus(v.OnSelfConnectionStatus, nil)
	t.CallbackFriendRequest(v.OnFriendRequest, nil)
	t.CallbackFriendMessage(v.OnFriendMessage, nil)
	t.CallbackFriendConnectionStatus(v.OnFriendConnectionStatus, nil)
	v.tox = t
	v.selfpk = t.SelfGetAddress()[:64]

	v.pk2pc.Store(make(pkPeerConnMap))
	v.client2pc.Store(make(peerConnMap))
	v.server2pc.Store(make(peerConnMap))
	reAddServerTimeout := v.ReAddServerTimeout
	if reAddServerTimeout == 0 {
		reAddServerTimeout = 10
	}
	var addservernext []*peerConn

	if started != nil {
		close(started)
	}

	v.running.Store(true)
	for v.running.Load().(bool) {
		t.Iterate()
		interval := time.Duration(t.IterationInterval()) * time.Millisecond
		start := time.Duration(time.Now().UnixNano())

		// send message
		for _, pc := range v.pk2pc.Load().(pkPeerConnMap) {
			if pc.online.Load().(bool) {
				for _, msg := range pc.signaling.get() {
					// msg always non-empty, so no err here
					t.FriendSendMessage(pc.friendNumber, string(msg))
				}
			}
		}

		// add server
		v.addpeerMu.Lock()
		addserver := v.addserver
		v.addserver = nil
		v.addpeerMu.Unlock()
		for _, cmd := range addserver {
			cmd.ch <- v.addServer(cmd.pc)
		}

		// send request for offline timeout servers
		for _, pc := range v.server2pc.Load().(peerConnMap) {
			if !pc.online.Load().(bool) {
				lastonline, err := t.FriendGetLastOnline(pc.friendNumber)
				if err == nil && lastonline > reAddServerTimeout {
					// TODO need to be tested
					v.Log.Debug("Server ReAddServerTimeout", zap.String("toxid", pc.peer.ToxID), zap.Uint64("lastonline", lastonline))
					ok, _ := t.FriendDelete(pc.friendNumber)
					if ok {
						v.numFriendRemoved(&v.server2pc, pc.friendNumber)
						v.addServer(pc)
					}
				}
			}
		}
		for _, pc := range addservernext {
			_ = pc.friendNumber
		}

		end := time.Duration(time.Now().UnixNano())
		time.Sleep(start + interval - end)
	}
	t.Kill()
	return nil
}

func (v *Conductor) Stop() {
	for _, pc := range v.pk2pc.Load().(pkPeerConnMap) {
		pc.connector.Close()
	}
	v.running.Store(false)
}

func (v *Conductor) AddServer(p *Server) (*hcrtc.RtcConnector, error) {
	friendId := string(p.ToxID[:64])
	if friendId == v.selfpk {
		return nil, ErrAddSelf
	}

	signaling := new(peerSignaling)
	connector, err := hcrtc.NewConnector(v.selfpk, friendId, signaling, v.Log)
	if err != nil {
		return nil, err
	}
	pc := &peerConn{
		peer:      *p,
		signaling: signaling,
		connector: connector,
	}
	pc.online.Store(false)

	ch := make(chan error, 1)
	v.addpeerMu.Lock()
	v.addserver = append(v.addserver, &addServerCommand{pc: pc, ch: ch})
	v.pkFriendAdded(&v.pk2pc, friendId, pc)
	v.addpeerMu.Unlock()

	err = <-ch
	if err != nil {
		connector.Close()
		return nil, err
	}
	return pc.connector, nil
}

// loop
func (v *Conductor) addServer(pc *peerConn) error {
	friendNumber, err := v.tox.FriendAdd(pc.peer.ToxID, pc.peer.Token)
	if err != nil {
		v.Log.Error("FriendAdd", zap.Error(err))
		return err
	}
	v.numFriendAdded(&v.server2pc, friendNumber, pc)
	return nil
}

// loop
func (v *Conductor) numFriendAdded(target *atomic.Value, friendNumber uint32, pc *peerConn) {
	pc.friendNumber = friendNumber
	m1 := target.Load().(peerConnMap)
	m2 := make(peerConnMap)
	for k, v := range m1 {
		m2[k] = v // copy all data from the current object to the new one
	}
	m2[friendNumber] = pc // do the update that we need
	target.Store(m2)      // atomically replace the current object with the new one
}

// lock
func (v *Conductor) pkFriendAdded(target *atomic.Value, pk string, pc *peerConn) {
	m1 := target.Load().(pkPeerConnMap)
	m2 := make(pkPeerConnMap)
	for k, v := range m1 {
		m2[k] = v // copy all data from the current object to the new one
	}
	m2[pk] = pc      // do the update that we need
	target.Store(m2) // atomically replace the current object with the new one
}

// loop
func (v *Conductor) numFriendRemoved(target *atomic.Value, friendNumber uint32) {
	m1 := target.Load().(peerConnMap)
	m2 := make(peerConnMap)
	for k, v := range m1 {
		if k != friendNumber {
			m2[k] = v // copy all data from the current object to the new one
		}
	}
	target.Store(m2) // atomically replace the current object with the new one
}

// lock
func (v *Conductor) pkFriendRemoved(target *atomic.Value, pk string) {
	m1 := target.Load().(pkPeerConnMap)
	m2 := make(pkPeerConnMap)
	for k, v := range m1 {
		if k != pk {
			m2[k] = v // copy all data from the current object to the new one
		}
	}
	target.Store(m2) // atomically replace the current object with the new one
}

func (v *Conductor) OnSelfConnectionStatus(t *tox.Tox, status int, userData interface{}) {
	if status != tox.CONNECTION_NONE {
		v.Log.Debug("OnSelfConnectionStatus online")
	} else {
		v.Log.Debug("OnSelfConnectionStatus offline")
		for _, pc := range v.pk2pc.Load().(pkPeerConnMap) {
			pc.online.Store(false)
		}
	}
}

func (v *Conductor) OnFriendRequest(t *tox.Tox, friendId string, message string, userData interface{}) {
	if _, ok := v.pk2pc.Load().(pkPeerConnMap)[friendId]; ok {
		return
	}
	if friendId == v.selfpk {
		return
	}
	if v.Validate != nil && !v.Validate(friendId, message) {
		return
	}

	signaling := new(peerSignaling)
	connector, err := hcrtc.NewConnector(v.selfpk, friendId, signaling, v.Log)
	if err != nil {
		return
	}
	friendNumber, err := v.tox.FriendAddNorequest(friendId)
	if err != nil {
		v.Log.Error("FriendAddNorequest", zap.Error(err))
		connector.Close()
		return
	}

	pc := &peerConn{
		signaling: signaling,
		connector: connector,
	}
	pc.online.Store(false)

	v.numFriendAdded(&v.client2pc, friendNumber, pc)

	v.addpeerMu.Lock()
	v.pkFriendAdded(&v.pk2pc, friendId, pc)
	v.addpeerMu.Unlock()

	if v.OnConnector != nil {
		v.OnConnector(connector)
	}
}

func (v *Conductor) OnFriendMessage(t *tox.Tox, friendNumber uint32, message string, userData interface{}) {
	pc, ok := v.client2pc.Load().(peerConnMap)[friendNumber]
	if !ok {
		pc, ok = v.server2pc.Load().(peerConnMap)[friendNumber]
	}
	if !ok {
		return
	}
	pc.connector.HandleSignalingMessage([]byte(message))
}

func (v *Conductor) OnFriendConnectionStatus(t *tox.Tox, friendNumber uint32, status int, userData interface{}) {
	pc, ok := v.client2pc.Load().(peerConnMap)[friendNumber]
	if !ok {
		pc, ok = v.server2pc.Load().(peerConnMap)[friendNumber]
	}
	if !ok {
		return
	}

	on := status != tox.CONNECTION_NONE
	pc.online.Store(on)
}
