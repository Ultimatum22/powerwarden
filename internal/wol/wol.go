// Package wol sends Wake-on-LAN magic packets to wake the Proxmox host
// across VLANs, via a broadcast, a unicast send to a static-ARP IP, or a
// router's own WoL API.
package wol

import (
	"bytes"
	"context"
	"fmt"
	"net"
)

// Sender wakes a host identified by mac. Implementations must not block
// waiting for the host to actually come up; the caller handles retries and
// timeouts (see the host state machine in internal/engine).
type Sender interface {
	Send(ctx context.Context, mac net.HardwareAddr) error
}

// MagicPacket builds the standard 102-byte Wake-on-LAN payload: six 0xFF
// bytes followed by the target MAC address repeated sixteen times.
func MagicPacket(mac net.HardwareAddr) ([]byte, error) {
	if len(mac) != 6 {
		return nil, fmt.Errorf("wol: mac must be 6 bytes, got %d", len(mac))
	}
	var buf bytes.Buffer
	buf.Write(bytes.Repeat([]byte{0xFF}, 6))
	for i := 0; i < 16; i++ {
		buf.Write(mac)
	}
	return buf.Bytes(), nil
}
