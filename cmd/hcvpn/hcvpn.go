package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/empirefox/cement/clog"
	"github.com/empirefox/honeycomb/hcvpn"
	"go.uber.org/zap"
)

func main() {
	cl, err := clog.NewLogger(clog.Config{
		Level: "debug",
	})
	if err != nil {
		panic(err)
	}

	fname := os.ExpandEnv("$HC_HOME/vpn.json")
	f, err := os.Open(fname)
	if err != nil {
		cl.Fatal("Cannot read config file", zap.String("file", fname), zap.Error(err))
	}

	var config hcvpn.Config
	err = json.NewDecoder(f).Decode(&config)
	if err != nil {
		cl.Fatal("Cannot read config json", zap.String("file", fname), zap.Error(err))
	}

	c, err := hcvpn.NewVpn(context.Background(), config, cl)
	if err != nil {
		return
	}
	defer c.Stop()
	c.Run()
}
