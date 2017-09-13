package hcpipe

import (
	"context"
	"net"

	"go.uber.org/zap"

	"github.com/keroserene/go-webrtc/data"
)

func chanFromConn(conn net.Conn, bufSize int, log *zap.Logger) chan []byte {
	c := make(chan []byte)

	go func() {
		defer func() {
			if err := recover(); err != nil {
				log.Error("chanFromConn recover", zap.Any("err", err))
			}
		}()

		b := make([]byte, bufSize)
		for {
			n, err := conn.Read(b)
			if n > 0 {
				c <- b[:n]
			}
			if err != nil {
				log.Warn("chanFromConn", zap.Error(err))
				c <- nil
				break
			}
		}
	}()

	return c
}

func chanFromRtc(channel *data.Channel, log *zap.Logger) chan []byte {
	c := make(chan []byte)

	channel.OnOpen = func() {
		log.Debug("channel.OnOpen")
	}
	channel.OnClose = func() {
		log.Debug("channel.OnClose")
		c <- nil
	}
	channel.OnMessage = func(b []byte) {
		c <- b
	}

	return c
}

// Pipe creates a full-duplex pipe between the two sockets and transfers data from one to the other.
func Pipe(ctx context.Context, conn net.Conn, channel *data.Channel, bufSize int, log *zap.Logger) {
	cc := chanFromConn(conn, bufSize, log)
	cr := chanFromRtc(channel, log)

	log.Debug("Pipe", zap.Stringer("local", conn.LocalAddr()), zap.Stringer("remote", conn.RemoteAddr()), zap.String("tox", channel.Label()))

	var sendQueue [][]byte

	defer func() {
		if err := recover(); err != nil {
			log.Error("pipe recover", zap.Any("err", err))
		} else {
			log.Debug("pipe closed")
		}
	}()

	for {
		select {
		case b := <-cc:
			if b == nil {
				return
			} else {
				switch channel.ReadyState() {
				case data.DataStateConnecting:
					sendQueue = append(sendQueue, b)

				case data.DataStateOpen:
					if len(sendQueue) != 0 {
						for _, msg := range sendQueue {
							channel.Send(msg)
						}
						sendQueue = nil
					}
					channel.Send(b)

				default:
					log.Info("pipe channel close")
					return
				}
			}
		case b := <-cr:
			if b == nil {
				return
			} else {
				if _, err := conn.Write(b); err != nil {
					log.Info("pipe conn.Write", zap.Error(err))
					return
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
