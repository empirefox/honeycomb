package hctox

import (
	"encoding/json"
	"os"

	"github.com/kitech/go-toxcore"
	"go.uber.org/zap"
)

type Node struct {
	Ipv4       string
	Ipv6       string
	Port       uint16
	TcpPorts   []uint16
	PublicKey  string `json:"public_key"`
	Maintainer string
	Location   string
	StatusUdp  bool
	StatusTcp  bool
	Version    string
	Motd       string
	LastPing   uint64
}

type Nodes struct {
	LastScan    uint64
	LastRefresh uint64
	Nodes       []Node
}

func (ns *Nodes) Bootstrap(t *tox.Tox, l *zap.Logger) {
	for _, node := range ns.Nodes {
		ok, err := t.Bootstrap(node.Ipv4, node.Port, node.PublicKey)
		l.Debug("bootstrap", zap.Error(err), zap.Bool("ok", ok))
		if node.StatusTcp {
			ok, err = t.AddTcpRelay(node.Ipv4, node.Port, node.PublicKey)
			l.Debug("bootstrap tcp", zap.Error(err), zap.Bool("ok", ok))
		}
	}
}

func ReadNodes(fname string, l *zap.Logger) (*Nodes, error) {
	fname = os.ExpandEnv(fname)
	file, err := os.Open(fname)
	if err != nil {
		l.Warn("fname", zap.Error(err), zap.String("file", fname))
		return nil, err
	}

	ns := new(Nodes)
	err = json.NewDecoder(file).Decode(ns)
	if err != nil {
		l.Warn("fname Decode", zap.Error(err), zap.String("file", fname))
		return nil, err
	}

	return ns, nil
}
