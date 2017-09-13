package hcpeer

import (
	"bufio"
	"bytes"
	"os"
	"strings"

	"go.uber.org/zap"
)

// 82:127.0.0.1:8282:6BA28AC06C1D57783FE017FA9322D0B356E61404C92155A04F64F3B19C75633E8BDDEFFA4856:SECRET
type PeerInfo struct {
	LocalPort    string
	HostOnRemote string
	PortOnRemote string
	ToxID        string
	Secret       string
	PubKey       string
	Super        bool
}

func ReadPeers(fname string, l *zap.Logger) (ps []PeerInfo) {
	fname = os.ExpandEnv(fname)
	file, err := os.Open(fname)
	if err != nil {
		l.Warn("ReadPeers", zap.Error(err), zap.String("file", fname))
		return
	}

	b := bufio.NewReader(file)

	var eof bool
	for !eof {
		line, err := b.ReadBytes('\n')
		if err != nil {
			eof = true
		}

		s := strings.Split(string(bytes.TrimSpace(line)), ":")
		if len(s) != 5 {
			continue
		}
		p := PeerInfo{
			LocalPort:    s[0],
			HostOnRemote: s[1],
			PortOnRemote: s[2],
			ToxID:        s[3],
			Secret:       s[4],
			PubKey:       string(s[3][:64]),
		}
		ps = append(ps, p)
	}

	return
}

func ReadSupers(fname string, l *zap.Logger) (ps []PeerInfo) {
	fname = os.ExpandEnv(fname)
	file, err := os.Open(fname)
	if err != nil {
		l.Warn("ReadSupers", zap.Error(err), zap.String("file", fname))
		return
	}

	b := bufio.NewReader(file)

	var eof bool
	for !eof {
		line, err := b.ReadBytes('\n')
		if err != nil {
			eof = true
		}

		s := strings.Split(string(bytes.TrimSpace(line)), ":")
		if len(s) != 2 {
			continue
		}
		p := PeerInfo{
			ToxID:  s[0],
			Secret: s[1],
			PubKey: string(s[0][:64]),
			Super:  true,
		}
		ps = append(ps, p)
	}

	return
}
