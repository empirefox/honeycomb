package hctox

import (
	"encoding/hex"
	"errors"
	"strings"

	"github.com/kitech/go-toxcore"
	"github.com/phayes/freeport"
)

func ToxID(secretKey string, nospam uint32) (string, error) {
	opt := tox.NewToxOptions()

	sk, err := hex.DecodeString(strings.ToLower(secretKey))
	if err != nil {
		return "", err
	}

	opt.Savedata_data = sk
	opt.Savedata_type = tox.SAVEDATA_TYPE_SECRET_KEY
	opt.Ipv6_enabled = false

	port, err := freeport.GetFreePort()
	if err != nil {
		return "", err
	}

	opt.Tcp_port = uint16(port)
	t := tox.NewTox(opt)
	if t == nil {
		return "", errors.New("NewTox failed")
	}

	defer t.Kill()

	t.SelfSetNospam(nospam)
	return t.SelfGetAddress(), nil
}
