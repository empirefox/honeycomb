package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"os"

	"go.uber.org/zap"

	"github.com/empirefox/cement/clog"
	"github.com/empirefox/honeycomb/hcpeer"
	"github.com/empirefox/honeycomb/hcrtc"
	"github.com/empirefox/honeycomb/hctox"
)

var servermode bool

func init() {
	flag.BoolVar(&servermode, "s", false, "server mode or not")
}

func main() {
	flag.Parse()

	cl, err := clog.NewLogger(clog.Config{
		Level: "debug",
	})
	if err != nil {
		panic(err)
	}

	log := cl.Module("hcpeer")

	if servermode {
		server(log)
	} else {
		client(log)
	}
}

func server(log *zap.Logger) {
	// ToxID: 9AA0FF6C243F90947035FA4AA45353A705B4F9681299AC61A295E3C32911EB63C8CA4CE7E9C1
	account, err := hctox.NewAccount("CF43FFE81487EA74A519C568E5D2CD79611D3661919617B9D8E542F4ECAB8977", 3368701159)
	if err != nil {
		panic(err)
	}

	listener := hcrtc.NewMergeListener(func(r *hcrtc.RtcConnector) {
		// TODO retry?
		log.Error("NewMergeListener: connector fail")
	})

	conductor := &hcpeer.Conductor{
		Log: log,

		Account: account,

		Validate: func(friendId, msg string) bool {
			return msg == "its me"
		},

		OnConnector: func(r *hcrtc.RtcConnector) { listener.Add(r) },
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				panic(err)
			}
			go func(conn net.Conn) {
				_, err := io.Copy(conn, io.TeeReader(conn, os.Stdout))
				if err != nil {
					panic(err)
				}
			}(conn)
		}
	}()

	err = conductor.Run(nil)
	if err != nil {
		panic(err)
	}
}

func client(log *zap.Logger) {
	// ToxID: F447C5472BDA9AC0DC98ACFE0E40D1434CC215CCF9D1729542A787BD0AEC5432AC0D40508AAC
	account, err := hctox.NewAccount("4C479CBC289E1085D90E5B951DCFBCA27940C3B9182AC79DDDAC1DE36CA2A967", 2886549584)
	if err != nil {
		panic(err)
	}

	conductor := &hcpeer.Conductor{
		Log: log,

		Account: account,

		Validate: func(friendId, msg string) bool {
			log.Error("Validate: should not be called")
			return false
		},

		OnConnector: func(r *hcrtc.RtcConnector) {
			log.Error("OnConnector: should not be called")
		},
	}

	started := make(chan struct{})
	go func() {
		<-started
		r, err := conductor.AddServer(&hcpeer.Server{
			ToxID: "9AA0FF6C243F90947035FA4AA45353A705B4F9681299AC61A295E3C32911EB63C8CA4CE7E9C1",
			Token: "its me",
		})
		if err != nil {
			panic(err)
		}

		conn, err := r.Dial()
		if err != nil {
			panic(err)
		}

		reader := bufio.NewReader(os.Stdin)
		for {
			fmt.Println("Enter text: ")
			text, err := reader.ReadBytes('\n')
			if err != nil {
				panic(err)
			}
			fmt.Println()
			n, err := conn.Write(text)
			if err != nil {
				panic(err)
			}
			b := make([]byte, n)
			_, err = conn.Read(b)
			if err != nil {
				panic(err)
			}
			fmt.Println("Server:")
			fmt.Println(string(b))
			fmt.Println()
		}
	}()

	err = conductor.Run(started)
	if err != nil {
		panic(err)
	}
}
