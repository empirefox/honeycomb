package hch2

import (
	"net"
	"net/http"
	"net/url"

	"github.com/empirefox/honeycomb/hcrtc"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
)

type Connector struct {
	log       *zap.Logger
	proxyHost string
	tr        *http2.Transport
	rtcc      *hcrtc.RtcConnector
}

func (c *Connector) Connect(cc net.Conn) error {
	req := http.Request{
		Method: "CONNECT",
		URL: &url.URL{
			Scheme: "http",
			Path:   "/",
			// Host => proxy
			Host: c.proxyHost,
		},
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
	}
	c.tr.RoundTrip()
}
