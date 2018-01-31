package hctox

import (
	"encoding/hex"
	"errors"
	"strings"

	"github.com/kitech/go-toxcore"
	"github.com/phayes/freeport"
)

var (
	ErrKeyLen    = errors.New("key len error")
	ErrNospam    = errors.New("spam required")
	ErrBootstrap = errors.New("Fail to bootstrap")
)

func NewTox(account *Account, tcpport uint16, threadSafe bool) (*tox.Tox, error) {
	opt := tox.NewToxOptions()

	if account.Nospam == 0 {
		return nil, ErrKeyLen
	}
	sk, err := hex.DecodeString(strings.ToLower(account.Secret))
	if err != nil {
		return nil, err
	}
	if len(sk) != 32 {
		return nil, ErrKeyLen
	}
	opt.Savedata_data = sk
	opt.Savedata_type = tox.SAVEDATA_TYPE_SECRET_KEY
	opt.ThreadSafe = threadSafe

	if tcpport == 0 {
		port, err := freeport.GetFreePort()
		if err != nil {
			return nil, err
		}
		opt.Tcp_port = uint16(port)
	} else {
		opt.Tcp_port = tcpport
	}

	t := tox.NewTox(opt)
	if t == nil && opt.Proxy_type != int32(tox.PROXY_TYPE_NONE) {
		tmp := opt.Proxy_type
		opt.Proxy_type = int32(tox.PROXY_TYPE_NONE)
		t = tox.NewTox(opt)
		if t == nil {
			opt.Proxy_type = tmp
		}
	}
	if t == nil && opt.Ipv6_enabled {
		opt.Ipv6_enabled = false
		t = tox.NewTox(opt)
		if t == nil {
			opt.Ipv6_enabled = true
		}
	}
	if t == nil {
		return nil, errors.New("NewTox failed")
	}

	t.SelfSetNospam(account.Nospam)

	n := 0
	for _, node := range ToxNodes {
		ok, _ := t.Bootstrap(node.Ipv4, node.Port, node.PublicKey)
		if ok {
			n++
		}
		if node.StatusTcp {
			t.AddTcpRelay(node.Ipv4, node.Port, node.PublicKey)
		}
	}
	if n == 0 {
		return nil, ErrBootstrap
	}

	defaultName := t.SelfGetName()
	humanName := "Hc." + t.SelfGetAddress()[0:5]
	if humanName != defaultName {
		t.SelfSetName(humanName)
	}

	return t, nil
}
