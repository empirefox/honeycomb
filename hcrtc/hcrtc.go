package hcrtc

import (
	"go.uber.org/zap"

	"github.com/empirefox/cement/clog"
	"github.com/keroserene/go-webrtc"
	"github.com/keroserene/go-webrtc/data"
)

const (
	RtcSdpTag      = 'S'
	RtcCadidateTag = 'C'
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

type SignalingServer interface {
	SignalingSend(msg []byte) error
	OnSignalingMessageFunc(func(msg []byte))
}

type BiChannel interface {
	OnIncome(channel *data.Channel)
}

type Callback interface {
	SignalingServer
	BiChannel
}

type RtcConnector struct {
	log *zap.Logger
	cb  Callback
	pc  *webrtc.PeerConnection
}

func NewConnector(cb Callback, cl clog.Logger) (*RtcConnector, error) {
	l := cl.Module("RTC")
	webrtc.SetLoggingVerbosity(1)
	l.Debug("Initiate a WebRTC PeerConnection...")

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
		l.Warn("Failed to create PeerConnection", zap.Error(err))
		return nil, err
	}

	r := &RtcConnector{
		log: l,
		cb:  cb,
		pc:  pc,
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
	pc.OnDataChannel = func(channel *data.Channel) { go cb.OnIncome(channel) }

	cb.OnSignalingMessageFunc(r.signalReceive)

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
		err := r.cb.SignalingSend(append([]byte{RtcSdpTag}, []byte(sdp)...))
		if err != nil {
			r.log.Error("generateOffer", zap.Error(err))
		}
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
		err := r.cb.SignalingSend(append([]byte{RtcSdpTag}, []byte(sdp)...))
		if err != nil {
			r.log.Error("generateAnswer", zap.Error(err))
		}
	}
}

func (r *RtcConnector) signalCandidate(candidate *webrtc.IceCandidate) {
	ice := candidate.Serialize()
	if ice != "" {
		err := r.cb.SignalingSend(append([]byte{RtcCadidateTag}, []byte(ice)...))
		if err != nil {
			r.log.Error("signalCandidate", zap.Error(err))
		}
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

func (r *RtcConnector) signalReceive(msg []byte) {
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

func (r *RtcConnector) CreateChannel() (*data.Channel, error) {
	// Attempting to create the first datachannel triggers ICE.
	r.log.Debug("Initializing datachannel")
	dc, err := r.pc.CreateDataChannel("", data.Init{})
	if err != nil {
		r.log.Error("Unexpected failure creating data.Channel", zap.Error(err))
		return nil, err
	}
	r.log.Debug("Initialize datachannel ok ok ok")
	return dc, nil
}

func (r *RtcConnector) Close() {
	r.pc.Close()
}
