package hcrtc

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/keroserene/go-webrtc"
)

const (
	RtcSdpTag      = 'S'
	RtcCadidateTag = 'C'
)

var (
	ErrPeerConnectionFailed = errors.New("PeerConnection Failed")
)

var IceServers = []string{
	"stun.l.google.com:19302",
	"stun1.l.google.com:19302",
	"stun2.l.google.com:19302",
	"stun3.l.google.com:19302",
	"stun4.l.google.com:19302",
	"stun.ekiga.net",
	"stun.ideasip.com",
	"stun.schlund.de",
	"stun.stunprotocol.org:3478",
	"stun.voiparound.com",
	"stun.voipbuster.com",
	"stun.voipstunt.com",
	"stun.voxgratia.org",
	"stun.services.mozilla.com",
}

type SignalingSender interface {
	SignalingSend(msg []byte)
}

type DataChannel struct {
	*webrtc.DataChannel
	done     chan struct{}
	doneOnce sync.Once
}

func NewDataChannel(dc *webrtc.DataChannel) *DataChannel {
	return &DataChannel{
		DataChannel: dc,
		done:        make(chan struct{}),
	}
}

func (dc *DataChannel) Done() {
	dc.doneOnce.Do(func() { close(dc.done) })
}

type RtcConnector struct {
	Local  string
	Remote string

	log      *zap.Logger
	ss       SignalingSender
	pc       *webrtc.PeerConnection
	dcs      chan *DataChannel
	pcfailed atomic.Value
	addr     net.Addr
}

func NewConnector(local, remote string, ss SignalingSender, log *zap.Logger) (*RtcConnector, error) {
	webrtc.SetLoggingVerbosity(1)

	config := webrtc.NewConfiguration()
	var servers []string
	for _, server := range IceServers {
		// MUST be stun
		servers = append(servers, "stun:"+server)
	}
	config.IceServers = []webrtc.IceServer{
		{Urls: servers},
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		log.Warn("Failed to create PeerConnection", zap.Error(err))
		return nil, err
	}

	r := &RtcConnector{
		log:    log,
		ss:     ss,
		pc:     pc,
		Local:  local,
		Remote: remote,
		dcs:    make(chan *DataChannel),
		addr:   newAddr(local),
	}

	// OnNegotiationNeeded is triggered when something important has occurred in
	// the state of PeerConnection (such as creating a new data channel), in which
	// case a new SDP offer must be prepared and sent to the remote peer.
	pc.OnNegotiationNeeded = func() { go r.generateOffer() }

	// Once ICE candidates are prepared, they need to be sent to the remote
	// peer which will attempt reaching the local peer through NATs.
	pc.OnIceCandidate = func(candidate webrtc.IceCandidate) { go r.signalCandidate(&candidate) }

	// A DataChannel is generated through this callback only when the remote peer
	// has initiated the creation of the data channel.
	pc.OnDataChannel = func(channel *webrtc.DataChannel) {
		dc := NewDataChannel(channel)
		r.dcs <- dc
		<-dc.done
	}

	r.pcfailed.Store(false)
	pc.OnConnectionStateChange = func(state webrtc.PeerConnectionState) {
		r.pcfailed.Store(state == webrtc.PeerConnectionStateFailed)
		log.Debug("Connection state changed", zap.Stringer("state", state), zap.String("remote", remote))
	}

	pc.OnSignalingStateChange = func(state webrtc.SignalingState) {
		log.Debug("Signal state changed", zap.Stringer("state", state), zap.String("remote", remote))
	}

	return r, nil
}

//
// Preparing SDP messages for signaling.
// generateOffer and generateAnswer are expected to be called within goroutines.
// It is possible to send the serialized offers or answers immediately upon
// creation, followed by subsequent individual ICE candidates.
//

func (r *RtcConnector) generateOffer() {
	r.log.Debug("Generating offer...")
	offer, err := r.pc.CreateOffer() // blocking
	if err != nil {
		r.log.Warn("CreateOffer", zap.Error(err))
		return
	}
	r.pc.SetLocalDescription(offer)

	// send offer
	sdp := offer.Serialize()
	if sdp != "" {
		r.ss.SignalingSend(append([]byte{RtcSdpTag}, []byte(sdp)...))
	}
}

func (r *RtcConnector) generateAnswer() {
	r.log.Debug("Generating answer...")
	answer, err := r.pc.CreateAnswer() // blocking
	if err != nil {
		r.log.Warn("CreateAnswer", zap.Error(err))
		return
	}
	r.pc.SetLocalDescription(answer)

	// send answer
	sdp := answer.Serialize()
	if sdp != "" {
		r.ss.SignalingSend(append([]byte{RtcSdpTag}, []byte(sdp)...))
	}
}

func (r *RtcConnector) signalCandidate(candidate *webrtc.IceCandidate) {
	ice := candidate.Serialize()
	if ice != "" {
		r.ss.SignalingSend(append([]byte{RtcCadidateTag}, []byte(ice)...))
	}
}

func (r *RtcConnector) onRemoteSdp(s []byte) {
	sdp := webrtc.DeserializeSessionDescription(string(s))
	if sdp == nil {
		r.log.Warn("Invalid SDP", zap.ByteString("sdp", s))
		return
	}

	err := r.pc.SetRemoteDescription(sdp)
	if err != nil {
		r.log.Error("SetRemoteDescription", zap.Error(err))
		return
	}
	r.log.Debug("SDP received", zap.String("type", sdp.Type))
	if "offer" == sdp.Type {
		go r.generateAnswer()
	}
}

func (r *RtcConnector) onRemoteCadidate(s []byte) {
	ice := webrtc.DeserializeIceCandidate(string(s))
	if ice == nil {
		r.log.Warn("Invalid ICE candidate")
		return
	}

	r.pc.AddIceCandidate(*ice)
	r.log.Debug("ICE candidate received")
}

func (r *RtcConnector) HandleSignalingMessage(msg []byte) {
	if len(msg) == 0 {
		return
	}

	switch msg[0] {
	case RtcSdpTag:
		r.onRemoteSdp(msg[1:])

	case RtcCadidateTag:
		r.onRemoteCadidate(msg[1:])

	default:
		r.log.Warn("Invalid signaling Message")
	}

}

func (r *RtcConnector) AcceptChannel() (*DataChannel, error) {
	for {
		select {
		case dc := <-r.dcs:
			return dc, nil
		case <-time.After(2 * time.Second):
			if r.pcfailed.Load().(bool) {
				return nil, ErrPeerConnectionFailed
			}
		}
	}
}

func (r *RtcConnector) CreateChannel() (*webrtc.DataChannel, error) {
	// Attempting to create the first datachannel triggers ICE.
	r.log.Debug("Initializing datachannel", zap.String("remote", r.Remote))
	dc, err := r.pc.CreateDataChannel(r.Remote)
	if err != nil {
		r.log.Error("Unexpected failure creating webrtc.DataChannel", zap.Error(err), zap.String("remote", r.Remote))
		return nil, err
	}
	r.log.Debug("Initialize datachannel ok", zap.String("remote", r.Remote))
	return dc, nil
}

func (r *RtcConnector) Accept() (net.Conn, error) {
	for {
		select {
		case dc := <-r.dcs:
			return NewConn(dc, r.Local, r.Remote, r.log), nil
		case <-time.After(2 * time.Second):
			if r.pcfailed.Load().(bool) {
				return nil, ErrPeerConnectionFailed
			}
		}
	}
}

func (r *RtcConnector) Dial() (net.Conn, error) {
	channel, err := r.CreateChannel()
	if err != nil {
		return nil, err
	}
	dc := NewDataChannel(channel)
	return NewConn(dc, r.Local, r.Remote, r.log), nil
}

func (r *RtcConnector) Close() error {
	r.pcfailed.Store(true)
	return r.pc.Close()
}

func (r *RtcConnector) Addr() net.Addr {
	return r.addr
}
