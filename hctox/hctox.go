package hctox

import (
	"errors"
	"io/ioutil"
	"math/rand"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/empirefox/cement/clog"
	"github.com/kitech/go-toxcore"
	"github.com/mcuadros/go-defaults"
)

type Callback interface {
	// Tox completed
	OnSelfConnectionStatus(status int)
	OnPeerMessage(friendId string, friendNumber uint32, message []byte)
	OnSuperMessage(friendId string, friendNumber uint32, message []byte)
	OnPeerOnline(friendId string, friendNumber uint32)
}

type Config struct {
	NamePrefix   string `default:"HcBot."`
	ToxsaveFile  string `default:"$HC_HOME/hc.toxsave"`
	ToxNodesFile string `default:"$HC_HOME/toxnodes.json"`
	Peers        map[string]string
	Supers       map[string]string
}

type Tox struct {
	log    *zap.Logger
	config *Config
	t      *tox.Tox
	cb     Callback

	ID string

	shutdown   bool
	shutdownMu sync.Mutex

	stopped chan struct{}
}

func NewTox(config Config, cb Callback, cl clog.Logger) (*Tox, error) {
	l := cl.Module("TOX")
	defaults.SetDefaults(&config)

	ns, err := ReadNodes(config.ToxNodesFile, l)
	if err != nil {
		return nil, err
	}

	toxsaveFile := os.ExpandEnv(config.ToxsaveFile)
	opt := tox.NewToxOptions()
	if tox.FileExist(toxsaveFile) {
		data, err := ioutil.ReadFile(toxsaveFile)
		if err != nil {
			l.Error("Read toxsave", zap.Error(err))
		} else {
			opt.Savedata_data = data
			opt.Savedata_type = tox.SAVEDATA_TYPE_TOX_SAVE
		}
	}

	r := rand.New(rand.NewSource(99))
	opt.Tcp_port = 1024 + uint16(r.Intn(64511))

	t := tox.NewTox(opt)
	if t == nil && opt.Proxy_type != tox.PROXY_TYPE_NONE {
		l.Error("trying without proxy")
		tmp := opt.Proxy_type
		opt.Proxy_type = tox.PROXY_TYPE_NONE
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

	ns.Bootstrap(t, l)

	toxid := t.SelfGetAddress()
	l.Warn("COPY THIS", zap.String("toxid", toxid))

	defaultName := t.SelfGetName()
	humanName := config.NamePrefix + toxid[0:5]
	if humanName != defaultName {
		t.SelfSetName(humanName)
	}
	humanName = t.SelfGetName()

	err = t.WriteSavedata(toxsaveFile)
	if err != nil {
		l.Warn("WriteSavedata", zap.String("file", toxsaveFile), zap.Error(err))
	}

	// add friend
	fv := t.SelfGetFriendList()
	for _, fno := range fv {
		fid, err := t.FriendGetPublicKey(fno)
		if err != nil {
			l.Warn("FriendGetPublicKey", zap.Error(err))
		} else {
			t.FriendAddNorequest(fid)
		}
	}

	peers := make(map[string]string)
	supers := make(map[string]string)
	for id, secret := range config.Peers {
		peers[string(id[:64])] = secret
		t.FriendAddNorequest(id)
		t.FriendAdd(id, secret)
	}
	for id, secret := range config.Supers {
		supers[string(id[:64])] = secret
		t.FriendAddNorequest(id)
		t.FriendAdd(id, secret)
	}
	l.Debug("Self info", zap.String("name", humanName), zap.Int("peers", len(peers)), zap.Int("supers", len(supers)))

	// callbacks
	t.CallbackSelfConnectionStatus(func(t *tox.Tox, status int, userData interface{}) {
		l.Debug("CallbackSelfConnectionStatus", zap.Int("status", status))
		go cb.OnSelfConnectionStatus(status)
	}, nil)
	t.CallbackFriendRequest(func(t *tox.Tox, friendId string, message string, userData interface{}) {
		if peers[friendId] != message && supers[friendId] != message {
			l.Debug("CallbackFriendRequest", zap.String("peer", friendId), zap.Error(err), zap.String("message", message))
			return
		}
		num, err := t.FriendAddNorequest(friendId)
		l.Debug("CallbackFriendRequest", zap.String("peer", friendId), zap.Error(err), zap.Uint32("total", num))
		if num < 100000 {
			t.WriteSavedata(toxsaveFile)
		}
	}, nil)
	t.CallbackFriendMessage(func(t *tox.Tox, friendNumber uint32, message string, userData interface{}) {
		friendId, err := t.FriendGetPublicKey(friendNumber)
		if err != nil {
			l.Warn("CallbackFriendMessage FriendGetPublicKey", zap.Uint32("friendNumber", friendNumber))
			return
		}
		if _, ok := peers[friendId]; ok {
			go cb.OnPeerMessage(friendId, friendNumber, []byte(message))
			return
		}
		if _, ok := supers[friendId]; ok {
			go cb.OnSuperMessage(friendId, friendNumber, []byte(message))
			return
		}
		// cb.OnFriendMessage(friendId, friendNumber, message)
	}, nil)
	t.CallbackFriendConnectionStatus(func(t *tox.Tox, friendNumber uint32, status int, userData interface{}) {
		friendId, err := t.FriendGetPublicKey(friendNumber)
		if err != nil {
			l.Warn("CallbackFriendConnectionStatus FriendGetPublicKey", zap.Uint32("friendNumber", friendNumber))
			return
		}
		l.Debug("CallbackFriendConnectionStatus", zap.String("peer", friendId), zap.Int("status", status))

		// online and peer and bigger toxid
		if _, ok := peers[friendId]; ok && status != tox.CONNECTION_NONE {
			go cb.OnPeerOnline(friendId, friendNumber)
		}
	}, nil)

	return &Tox{
		log:    l,
		config: &config,
		t:      t,
		cb:     cb,
		ID:     toxid,
	}, nil
}

func (wrap *Tox) Send(friendNumber uint32, message []byte) error {
	_, err := wrap.t.FriendSendMessage(friendNumber, string(message))
	if err != nil {
		wrap.log.Error("FriendSendMessage", zap.Uint32("friendNumber", friendNumber))
	}
	return err
}

func (wrap *Tox) StopAndKill() chan struct{} {
	wrap.shutdownMu.Lock()
	defer wrap.shutdownMu.Unlock()
	if !wrap.shutdown {
		wrap.shutdown = true
	}
	return wrap.stopped
}

func (wrap *Tox) stopping() bool {
	wrap.shutdownMu.Lock()
	defer wrap.shutdownMu.Unlock()
	return wrap.shutdown
}

// Run toxcore loops
func (wrap *Tox) Run() {
	wrap.shutdownMu.Lock()
	wrap.shutdown = false
	wrap.stopped = make(chan struct{})
	wrap.shutdownMu.Unlock()

	t := wrap.t
	for !wrap.stopping() {
		t.Iterate()
		time.Sleep(time.Duration(t.IterationInterval()) * time.Millisecond)
	}
	t.Kill()
	wrap.log.Debug("Tox stopped")
	close(wrap.stopped)
}
