package hcpeer

import (
	"sync"

	"github.com/empirefox/honeycomb/hcrtc"
)

type peerSignaling struct {
	unsent   [][]byte
	unsentMu sync.Mutex
}

func (ps *peerSignaling) SignalingSend(msg []byte) {
	ps.unsentMu.Lock()
	defer ps.unsentMu.Unlock()
	if msg[0] == hcrtc.RtcSdpTag {
		ps.unsent = [][]byte{msg}
	} else {
		ps.unsent = append(ps.unsent, msg)
	}
}

// loop
func (ps *peerSignaling) get() [][]byte {
	ps.unsentMu.Lock()
	defer ps.unsentMu.Unlock()
	unsent := ps.unsent
	ps.unsent = nil
	return unsent
}
