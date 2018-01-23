package hcvpn

import (
	//	"golang.org/x/net/ipv4"

	"go.uber.org/zap"

	"github.com/songgao/water"
	"github.com/vishvananda/netlink"
)

const (
	// MTU used for tunneled packets
	MTU = 1500
)

// ifaceSetup returns new interface!
// From https://github.com/kanocz/lcvpn
func (v *Vpn) ifaceSetup() (*water.Interface, netlink.Link, error) {
	addr, err := netlink.ParseAddr(v.cidr)
	if nil != err {
		v.log.Warn("ParseAddr", zap.Error(err), zap.String("cidr", v.cidr))
		return nil, nil, err
	}

	iconfig := water.Config{
		DeviceType: water.TUN,
	}
	iconfig.Name = v.config.TUNName
	iface, err := water.New(iconfig)

	if nil != err {
		v.log.Error("NewTUN", zap.Error(err), zap.String("name", v.config.TUNName))
		return nil, nil, err
	}

	ifaceName := iface.Name()
	v.log.Debug("TUN on interface", zap.String("iface", ifaceName))

	link, err := netlink.LinkByName(ifaceName)
	if nil != err {
		v.log.Error("LinkByName", zap.Error(err), zap.String("iface", ifaceName))
		return nil, nil, err
	}

	err = netlink.LinkSetMTU(link, MTU)
	if nil != err {
		v.log.Error("LinkSetMTU", zap.Error(err), zap.Int("MTU", MTU))
		return nil, nil, err
	}

	err = netlink.AddrAdd(link, addr)
	if nil != err {
		v.log.Error("AddrAdd", zap.Error(err), zap.String("cidr", v.cidr))
		return nil, nil, err
	}

	err = netlink.LinkSetUp(link)
	if nil != err {
		v.log.Error("LinkSetUp", zap.Error(err))
		return nil, nil, err
	}

	return iface, link, nil
}

// iface writing is in peerconn gorouting
func (v *Vpn) IfaceReading() {
	var packet IPPacket = make([]byte, MTU)

	for {
		_, err := v.iface.Read([]byte(packet))
		if err != nil {
			v.log.Error("iface.Read", zap.Error(err))
			break
		}

		if 4 != packet.IPver() {
			// header, _ := ipv4.ParseHeader(packet)
			// v.log.Debug("Non IPv4 packet", zap.Stringer("header", header))
			continue
		}

		dst := packet.Dst()

		pcs := v.ip2pc.Load().(ip2pc)

		// send ip_data to ip
		pc, ok := pcs[dst]
		if ok {
			pc.c.Write([]byte(packet))
			continue
		}

		// Broadcast
		if dst == v.bcastIP || packet.IsMulticast() {
			for _, pc := range pcs {
				pc.c.Write([]byte(packet))
			}
			continue
		}

		// send net_data to ip
		dstip := packet.DstV4()
		for _, pc := range pcs {
			for _, route := range pc.routes {
				if route.Dst.Contains(dstip) {
					pc.c.Write([]byte(packet))
					continue
				}
			}
		}

		// ignore
		// v.log.Debug("Unknown dst", zap.Stringer("header", header))
	}
}
