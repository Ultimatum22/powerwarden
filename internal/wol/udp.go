package wol

import (
	"context"
	"fmt"
	"net"
)

const defaultPort = 9

// UnicastSender sends the magic packet as a single UDP datagram to a
// specific IP, e.g. a static-ARP entry on the router so the frame reaches
// the target VLAN without normal ARP resolution (the host is off, so it
// cannot answer ARP itself).
type UnicastSender struct {
	// Target is the destination IP (no port; UDP port 9 is used).
	Target string
	// Dial is overridable in tests; defaults to net.Dial.
	Dial func(network, address string) (net.Conn, error)
}

func (s UnicastSender) Send(ctx context.Context, mac net.HardwareAddr) error {
	return sendPacket(ctx, s.dial(), "udp", fmt.Sprintf("%s:%d", s.Target, defaultPort), mac)
}

func (s UnicastSender) dial() func(network, address string) (net.Conn, error) {
	if s.Dial != nil {
		return s.Dial
	}
	return net.Dial
}

// BroadcastSender sends the magic packet to a broadcast address (e.g.
// 10.22.10.255) on the target VLAN.
type BroadcastSender struct {
	// Target is the broadcast IP (no port; UDP port 9 is used).
	Target string
	Dial   func(network, address string) (net.Conn, error)
}

func (s BroadcastSender) Send(ctx context.Context, mac net.HardwareAddr) error {
	return sendPacket(ctx, s.dial(), "udp", fmt.Sprintf("%s:%d", s.Target, defaultPort), mac)
}

func (s BroadcastSender) dial() func(network, address string) (net.Conn, error) {
	if s.Dial != nil {
		return s.Dial
	}
	return net.Dial
}

func sendPacket(ctx context.Context, dial func(network, address string) (net.Conn, error), network, addr string, mac net.HardwareAddr) error {
	packet, err := MagicPacket(mac)
	if err != nil {
		return err
	}
	conn, err := dial(network, addr)
	if err != nil {
		return fmt.Errorf("wol: dial %s: %w", addr, err)
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetWriteDeadline(dl); err != nil {
			return fmt.Errorf("wol: set write deadline: %w", err)
		}
	}

	n, err := conn.Write(packet)
	if err != nil {
		return fmt.Errorf("wol: write to %s: %w", addr, err)
	}
	if n != len(packet) {
		return fmt.Errorf("wol: short write to %s: wrote %d of %d bytes", addr, n, len(packet))
	}
	return nil
}
