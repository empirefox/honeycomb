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

	var config hctox.Config
	if servermode {
		config = serverConfig()
	} else {
		config = clientConfig()
	}

	conductor, err := hcpeer.NewConductor(config, cl)
	if err != nil {
		cl.Fatal("NewConductor", zap.Error(err))
	}

	go conductor.Run()

	if servermode {
		serverConn(conductor)
	} else {
		clientConn(conductor)
	}
}

func serverConfig() hctox.Config {
	config := hctox.Config{
		// ToxID: 9AA0FF6C243F90947035FA4AA45353A705B4F9681299AC61A295E3C32911EB63C8CA4CE7E9C1
		ToxSecretKey: "CF43FFE81487EA74A519C568E5D2CD79611D3661919617B9D8E542F4ECAB8977",
		ToxNospam:    3368701159,
		Actives: []hctox.Peer{
			{Name: "client1", ToxID: "F447C5472BDA9AC0DC98ACFE0E40D1434CC215CCF9D1729542A787BD0AEC5432AC0D40508AAC", Secret: "9AA0FF6C243F90947035FA4A"},
		},
	}
	return config
}

func clientConfig() hctox.Config {
	config := hctox.Config{
		// ToxID: F447C5472BDA9AC0DC98ACFE0E40D1434CC215CCF9D1729542A787BD0AEC5432AC0D40508AAC
		ToxSecretKey: "4C479CBC289E1085D90E5B951DCFBCA27940C3B9182AC79DDDAC1DE36CA2A967",
		ToxNospam:    2886549584,
		Passives: []hctox.Peer{
			{Name: "area1", ToxID: "9AA0FF6C243F90947035FA4AA45353A705B4F9681299AC61A295E3C32911EB63C8CA4CE7E9C1", Secret: "9AA0FF6C243F90947035FA4A"},
		},
	}
	return config
}

func serverConn(conductor *hcpeer.Conductor) {
	for {
		conn, err := conductor.Accept("client1")
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
}

func clientConn(conductor *hcpeer.Conductor) {
	conn, err := conductor.Dial("area1")
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
}
