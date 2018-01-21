package hctox

import (
	"encoding/hex"
	"errors"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/empirefox/cement/clog"
	"github.com/kitech/go-toxcore"
	"github.com/mcuadros/go-defaults"
)

type Callback interface {
	OnSelfConnectionStatus(status int)
	OnActiveAdded(friendId string, friendNumber uint32)
	OnPassiveAdded(friendId string, friendNumber uint32)
	OnToxMessage(friendNumber uint32, message []byte)
	OnToxOnline(friendNumber uint32, on bool)
}

type Peer struct {
	Name   string `validate:"required,lte=127"` // TOX_MAX_NAME_LENGTH 128
	ToxID  string `validate:"len=76"`
	Secret string `validate:"gte=16,lte=1015"`
}

type Config struct {
	NamePrefix   string `validate:"lte=122" default:"HcBot."` // TOX_MAX_NAME_LENGTH 128
	ToxSecretKey string `validate:"len=64"`
	ToxNospam    uint32 `validate:"required"`

	// do not send friend request to list
	// accept from list
	// reject others
	Actives []Peer

	// send friend request to list
	// only accept from list
	Passives []Peer
}

type Tox struct {
	log     *zap.Logger
	config  *Config
	t       *tox.Tox
	cb      Callback
	running atomic.Value

	ID string
}

func NewTox(config Config, cb Callback, cl clog.Logger) (*Tox, error) {
	l := cl.Module("hctox")
	defaults.SetDefaults(&config)
	if config.Actives == nil {
		config.Actives = make([]Peer, 0)
	}
	if config.Passives == nil {
		config.Passives = make([]Peer, 0)
	}

	opt := tox.NewToxOptions()

	sk, err := hex.DecodeString(strings.ToLower(config.ToxSecretKey))
	if err != nil {
		l.Error("ToxSecretKey decode hex", zap.Error(err))
		return nil, err
	}
	opt.Savedata_data = sk
	opt.Savedata_type = tox.SAVEDATA_TYPE_SECRET_KEY

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	opt.Tcp_port = 1024 + uint16(r.Intn(64511))
	l.Debug("Tox tcp", zap.Uint16("port", opt.Tcp_port))

	t := tox.NewTox(opt)
	if t == nil && opt.Proxy_type != int32(tox.PROXY_TYPE_NONE) {
		l.Error("trying without proxy")
		tmp := opt.Proxy_type
		opt.Proxy_type = int32(tox.PROXY_TYPE_NONE)
		t = tox.NewTox(opt)
		if t == nil {
			opt.Proxy_type = tmp
		}
	}
	if t == nil && opt.Ipv6_enabled {
		l.Error("trying without IPv6")
		opt.Ipv6_enabled = false
		t = tox.NewTox(opt)
		if t == nil {
			opt.Ipv6_enabled = true
		}
	}
	if t == nil {
		return nil, errors.New("NewTox failed")
	}

	t.SelfSetNospam(config.ToxNospam)

	for _, node := range ToxNodes {
		ok, err := t.Bootstrap(node.Ipv4, node.Port, node.PublicKey)
		l.Debug("bootstrap", zap.Error(err), zap.Bool("ok", ok))
		if node.StatusTcp {
			ok, err = t.AddTcpRelay(node.Ipv4, node.Port, node.PublicKey)
			l.Debug("bootstrap tcp", zap.Error(err), zap.Bool("ok", ok))
		}
	}

	toxid := t.SelfGetAddress()
	l.Warn("VALIDATE THIS", zap.String("toxid", toxid))

	defaultName := t.SelfGetName()
	humanName := config.NamePrefix + toxid[0:5]
	if humanName != defaultName {
		t.SelfSetName(humanName)
	}
	humanName = t.SelfGetName()

	activesIds := make(map[string]string)
	passivesIds := make(map[string]struct{})
	for _, p := range config.Actives {
		activesIds[string(p.ToxID[:64])] = p.Secret
	}
	for _, p := range config.Passives {
		passivesIds[string(p.ToxID[:64])] = struct{}{}
	}
	l.Debug("Self info",
		zap.String("name", humanName),
		zap.Int("actives", len(activesIds)),
		zap.Int("passives", len(passivesIds)))

	peers := make(map[uint32]struct{})

	var peerOnce sync.Once
	// callbacks
	t.CallbackSelfConnectionStatus(func(t *tox.Tox, status int, userData interface{}) {
		if status != tox.CONNECTION_NONE {
			peerOnce.Do(func() {
				for _, p := range config.Passives {
					num, err := t.FriendAdd(p.ToxID, p.Secret)
					if err != nil {
						l.Error("FriendAdd", zap.String("Passive", p.ToxID), zap.Error(err))
						continue
					}
					peers[num] = struct{}{}
					go cb.OnPassiveAdded(p.ToxID[:64], num)
				}
			})
		}
		cb.OnSelfConnectionStatus(status)
	}, nil)
	t.CallbackFriendRequest(func(t *tox.Tox, friendId string, message string, userData interface{}) {
		if _, ok := passivesIds[friendId]; ok {
			return
		}
		if ak, ok := activesIds[friendId]; ok {
			if message == ak {
				num, err := t.FriendAddNorequest(friendId)
				if err != nil {
					l.Error("FriendAddNorequest", zap.String("Active", friendId), zap.Error(err))
					return
				}
				peers[num] = struct{}{}
				go cb.OnActiveAdded(friendId, num)
				return
			}
			l.Debug("CallbackFriendRequest reject", zap.String("Active", friendId))
			return
		}
		l.Debug("CallbackFriendRequest ignore unknown", zap.String("peer", friendId))
	}, nil)
	t.CallbackFriendMessage(func(t *tox.Tox, friendNumber uint32, message string, userData interface{}) {
		if _, ok := peers[friendNumber]; ok {
			go cb.OnToxMessage(friendNumber, []byte(message))
			return
		}
	}, nil)
	t.CallbackFriendConnectionStatus(func(t *tox.Tox, friendNumber uint32, status int, userData interface{}) {
		if _, ok := peers[friendNumber]; ok {
			go cb.OnToxOnline(friendNumber, status != tox.CONNECTION_NONE)
		}
	}, nil)

	wrap := &Tox{
		log:    l,
		config: &config,
		t:      t,
		cb:     cb,
		ID:     toxid,
	}
	wrap.running.Store(false)
	return wrap, nil
}

func (wrap *Tox) Send(friendNumber uint32, message []byte) error {
	_, err := wrap.t.FriendSendMessage(friendNumber, string(message))
	if err != nil {
		wrap.log.Error("FriendSendMessage", zap.Uint32("friendNumber", friendNumber))
	}
	return err
}

func (wrap *Tox) Stop() {
	wrap.running.Store(false)
}

// Run toxcore loops
func (wrap *Tox) Run() {
	wrap.running.Store(true)
	t := wrap.t
	for wrap.running.Load().(bool) {
		t.Iterate()
		time.Sleep(time.Duration(t.IterationInterval()) * time.Millisecond)
	}
	t.Kill()
	wrap.log.Debug("Tox stopped")
}
