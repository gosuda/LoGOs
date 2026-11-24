// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quic

import (
	"net"
	"net/netip"
)

// netPacketConn is a packetConn implementation wrapping a net.PacketConn.
//
// PacketConn does not report the destination address of packets received on a
// wildcard socket.
type netPacketConn struct {
	c         net.PacketConn
	localAddr netip.AddrPort
}

func newNetPacketConn(pc net.PacketConn) (*netPacketConn, error) {
	addr, err := addrPortFromAddr(pc.LocalAddr())
	if err != nil {
		return nil, err
	}
	return &netPacketConn{
		c:         pc,
		localAddr: addr,
	}, nil
}

func (c *netPacketConn) Close() error {
	return c.c.Close()
}

func (c *netPacketConn) LocalAddr() netip.AddrPort {
	return c.localAddr
}

func (c *netPacketConn) Read(f func(*datagram)) error {
	localAddr := c.localAddr
	if localAddr.Addr().Unmap().IsUnspecified() {
		localAddr = netip.AddrPortFrom(netip.Addr{}, localAddr.Port())
	}
	for {
		dgram := newDatagram()
		n, peerAddr, err := c.c.ReadFrom(dgram.b)
		if err != nil {
			dgram.recycle()
			return err
		}
		dgram.peerAddr, err = addrPortFromAddr(peerAddr)
		if err != nil {
			dgram.recycle()
			continue
		}
		dgram.localAddr = localAddr
		dgram.b = dgram.b[:n]
		f(dgram)
	}
}

func (c *netPacketConn) Write(dgram datagram) error {
	_, err := c.c.WriteTo(dgram.b, net.UDPAddrFromAddrPort(dgram.peerAddr))
	return err
}

func addrPortFromAddr(addr net.Addr) (netip.AddrPort, error) {
	switch a := addr.(type) {
	case *net.UDPAddr:
		return a.AddrPort(), nil
	}
	return netip.ParseAddrPort(addr.String())
}
