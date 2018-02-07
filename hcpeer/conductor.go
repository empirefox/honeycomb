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
	online       bool
	lastonline   uint64
	signaling    *peerSignaling
	connector    *hcrtc.RtcConnector
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
	pk2pc pkPeerConnMap

	// peerConnMap
	client2pc peerConnMap
	server2pc peerConnMap

	running atomic.Value

	mu sync.Mutex
}

func (v *Conductor) Connector(pk string) (*hcrtc.RtcConnector, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if pc, ok := v.pk2pc[pk]; ok {
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

	v.pk2pc = make(pkPeerConnMap)
	v.client2pc = make(peerConnMap)
	v.server2pc = make(peerConnMap)

	reAddServerTimeout := v.ReAddServerTimeout
	if reAddServerTimeout == 0 {
		reAddServerTimeout = 30
	}

	if started != nil {
		close(started)
	}

	// TODO need test here
	resendrequest := true

	v.running.Store(true)
	for v.running.Load().(bool) {
		v.mu.Lock()
		t.Iterate()
		interval := time.Duration(t.IterationInterval()) * time.Millisecond
		start := time.Duration(time.Now().UnixNano())

		now := uint64(start / time.Second)
		// renew client pc
		for friendNumber, pc := range v.client2pc {
			select {
			case <-pc.connector.CloseNotify():
				v.renewClientPeerConn(friendNumber)
			default:
			}
		}

		// send request for offline timeout servers
		if resendrequest {
			for friendNumber, pc := range v.server2pc {
				select {
				case <-pc.connector.CloseNotify():
					v.renewServerPeerConn(friendNumber)
				default:
				}
				if !pc.online {
					lastonline, err := t.FriendGetLastOnline(friendNumber)
					if err == nil && now-lastonline > reAddServerTimeout && lastonline != pc.lastonline {
						// TODO need to be tested
						v.Log.Debug("ReAddServerTimeout", zap.String("toxid", pc.peer.ToxID), zap.Uint64("lastonline", lastonline))
						ok, _ := t.FriendDelete(friendNumber)
						if ok {
							delete(v.server2pc, friendNumber)
							v.addServer(pc)
							pc.lastonline = lastonline
						}
					}
				}
			}
		}

		// send message
		for _, pc := range v.pk2pc {
			if pc.online {
				for _, msg := range pc.signaling.get() {
					// msg always non-empty, so no err here
					t.FriendSendMessage(pc.friendNumber, string(msg))
				}
			}
		}
		v.mu.Unlock()

		end := time.Duration(time.Now().UnixNano())
		time.Sleep(start + interval - end)
	}
	t.Kill()
	return nil
}

func (v *Conductor) Stop() {
	v.mu.Lock()
	for _, pc := range v.pk2pc {
		pc.connector.Close()
		v.pk2pc = make(pkPeerConnMap)
		v.client2pc = make(peerConnMap)
		v.server2pc = make(peerConnMap)
	}
	v.mu.Unlock()

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

	v.mu.Lock()
	defer v.mu.Unlock()

	v.pk2pc[friendId] = pc
	err = v.addServer(pc)
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
	pc.friendNumber = friendNumber
	v.server2pc[friendNumber] = pc
	return nil
}

// loop
func (v *Conductor) renewClientPeerConn(friendNumber uint32) {
	pc, ok := v.client2pc[friendNumber]
	if !ok {
		return
	}
	old := pc.connector

	signaling := new(peerSignaling)
	connector, err := hcrtc.NewConnector(v.selfpk, old.Remote, signaling, v.Log)
	if err != nil {
		v.Log.Error("NewConnector", zap.Error(err))
		return
	}
	pc.signaling = signaling
	pc.connector = connector
	if v.OnConnector != nil {
		v.OnConnector(connector)
	}
	old.Close()
}

// loop
func (v *Conductor) renewServerPeerConn(friendNumber uint32) {
	pc, ok := v.server2pc[friendNumber]
	if !ok {
		return
	}
	old := pc.connector

	signaling := new(peerSignaling)
	connector, err := hcrtc.NewConnector(v.selfpk, old.Remote, signaling, v.Log)
	if err != nil {
		v.Log.Error("NewConnector", zap.Error(err))
		return
	}
	pc.signaling = signaling
	pc.connector = connector
	old.Close()
}

func (v *Conductor) OnSelfConnectionStatus(t *tox.Tox, status int, userData interface{}) {
	if status != tox.CONNECTION_NONE {
		v.Log.Debug("OnSelfConnectionStatus online")
		for friendNumber, pc := range v.client2pc {
			status, err := t.FriendGetConnectionStatus(friendNumber)
			if err == nil {
				pc.online = status != tox.CONNECTION_NONE
			}
		}
		for friendNumber, pc := range v.server2pc {
			status, err := t.FriendGetConnectionStatus(friendNumber)
			if err == nil {
				pc.online = status != tox.CONNECTION_NONE
			}
		}
	} else {
		v.Log.Debug("OnSelfConnectionStatus offline")
		for _, pc := range v.pk2pc {
			pc.online = false
		}
	}
}

func (v *Conductor) OnFriendRequest(t *tox.Tox, friendId string, message string, userData interface{}) {
	if _, ok := v.pk2pc[friendId]; ok {
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
		friendNumber: friendNumber,
		signaling:    signaling,
		connector:    connector,
	}

	v.client2pc[friendNumber] = pc
	v.pk2pc[friendId] = pc

	if v.OnConnector != nil {
		v.OnConnector(connector)
	}
}

func (v *Conductor) OnFriendMessage(t *tox.Tox, friendNumber uint32, message string, userData interface{}) {
	pc, ok := v.client2pc[friendNumber]
	if !ok {
		pc, ok = v.server2pc[friendNumber]
	}
	if !ok {
		return
	}
	pc.connector.HandleSignalingMessage([]byte(message))
}

func (v *Conductor) OnFriendConnectionStatus(t *tox.Tox, friendNumber uint32, status int, userData interface{}) {
	v.Log.Debug("OnFriendConnectionStatus", zap.Int("status", status))
	pc, ok := v.client2pc[friendNumber]
	if !ok {
		pc, ok = v.server2pc[friendNumber]
	}
	if !ok {
		return
	}

	on := status != tox.CONNECTION_NONE
	pc.online = on
}
