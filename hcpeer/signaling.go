package hcpeer

import (
	"sync"

	"github.com/empirefox/honeycomb/hcrtc"
	"github.com/empirefox/honeycomb/hctox"
)

type PeerSignaling struct {
	tox          *hctox.Tox
	friendNumber uint32
	pubkey       string
	usable       bool
	unsent       [][]byte
	unsentMu     sync.Mutex
}

func NewSignaling(tox *hctox.Tox, pubkey string) *PeerSignaling {
	ps := &PeerSignaling{
		tox:    tox,
		pubkey: pubkey,
	}
	return ps
}

func (ps *PeerSignaling) SignalingSend(msg []byte) {
	ps.unsentMu.Lock()
	defer ps.unsentMu.Unlock()

	if !ps.usable {
		ps.appendUnsent(msg)
		return
	}
	if len(ps.unsent) > 0 {
		ps.appendUnsent(msg)
		ps.tryUnsent()
		return
	}
	if err := ps.tox.Send(ps.friendNumber, msg); err != nil {
		ps.appendUnsent(msg)
	}

}

func (ps *PeerSignaling) Usable(u bool, friendNumber uint32) {
	ps.unsentMu.Lock()
	defer ps.unsentMu.Unlock()

	ps.usable = u
	ps.friendNumber = friendNumber
	if u {
		ps.tryUnsent()
	}
}

func (ps *PeerSignaling) IsUsable() bool {
	ps.unsentMu.Lock()
	defer ps.unsentMu.Unlock()
	return ps.usable
}

func (ps *PeerSignaling) ClearUnsent() {
	ps.unsentMu.Lock()
	defer ps.unsentMu.Unlock()
	ps.unsent = nil
}

func (ps *PeerSignaling) appendUnsent(msg []byte) {
	if msg[0] == hcrtc.RtcSdpTag {
		ps.unsent = [][]byte{msg}
	} else {
		ps.unsent = append(ps.unsent, msg)
	}
}

func (ps *PeerSignaling) tryUnsent() {
	if len(ps.unsent) > 0 {
		for i, msg := range ps.unsent {
			if err := ps.tox.Send(ps.friendNumber, msg); err != nil {
				ps.unsent = ps.unsent[i:]
				break
			}
		}
	}
}

//func (ps *PeerSignaling) cleanUnsent() {
//	if len(ps.unsent) > 0 {
//		for i := len(ps.unsent) - 1; i >= 0; i-- {
//			if ps.unsent[i][0] == hcrtc.RtcSdpTag {
//				ps.unsent = ps.unsent[i:]
//			}
//		}
//	}
//}
