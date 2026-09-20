package wol

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustMAC(t *testing.T, s string) net.HardwareAddr {
	t.Helper()
	mac, err := net.ParseMAC(s)
	if err != nil {
		t.Fatalf("ParseMAC: %v", err)
	}
	return mac
}

func TestMagicPacketShape(t *testing.T) {
	mac := mustMAC(t, "aa:bb:cc:dd:ee:ff")
	packet, err := MagicPacket(mac)
	if err != nil {
		t.Fatalf("MagicPacket: %v", err)
	}
	if len(packet) != 102 {
		t.Fatalf("packet length = %d, want 102", len(packet))
	}
	if !bytes.Equal(packet[:6], bytes.Repeat([]byte{0xFF}, 6)) {
		t.Fatalf("packet header = % x, want six 0xFF bytes", packet[:6])
	}
	for i := 0; i < 16; i++ {
		got := packet[6+i*6 : 6+i*6+6]
		if !bytes.Equal(got, []byte(mac)) {
			t.Fatalf("repetition %d = % x, want % x", i, got, []byte(mac))
		}
	}
}

func TestMagicPacketRejectsShortMAC(t *testing.T) {
	if _, err := MagicPacket(net.HardwareAddr{0x01, 0x02}); err == nil {
		t.Fatal("expected error for short MAC")
	}
}

// fakeUDPListener records datagrams sent to it, standing in for "a real
// router, ARP table, or Proxmox host" per CLAUDE.md's testing rule.
func fakeUDPListener(t *testing.T) (addr string, received func() [][]byte) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	var mu sync.Mutex
	var packets [][]byte
	go func() {
		buf := make([]byte, 512)
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			cp := make([]byte, n)
			copy(cp, buf[:n])
			mu.Lock()
			packets = append(packets, cp)
			mu.Unlock()
		}
	}()

	return conn.LocalAddr().String(), func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		out := make([][]byte, len(packets))
		copy(out, packets)
		return out
	}
}

func waitForPackets(t *testing.T, received func() [][]byte, want int) [][]byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pkts := received(); len(pkts) >= want {
			return pkts
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d packet(s), got %d", want, len(received()))
	return nil
}

func TestUnicastSenderSendsCorrectPacket(t *testing.T) {
	addr, received := fakeUDPListener(t)

	mac := mustMAC(t, "aa:bb:cc:dd:ee:ff")
	sender := UnicastSender{
		Target: "127.0.0.1", // the real address is supplied via Dial below, since defaultPort (9) isn't the fake listener's port
		Dial: func(network, _ string) (net.Conn, error) {
			return net.Dial(network, addr)
		},
	}
	if err := sender.Send(context.Background(), mac); err != nil {
		t.Fatalf("Send: %v", err)
	}

	packets := waitForPackets(t, received, 1)
	want, _ := MagicPacket(mac)
	if !bytes.Equal(packets[0], want) {
		t.Fatalf("received packet = % x, want % x", packets[0], want)
	}
}

func TestBroadcastSenderSendsCorrectPacket(t *testing.T) {
	addr, received := fakeUDPListener(t)
	mac := mustMAC(t, "11:22:33:44:55:66")
	sender := BroadcastSender{
		Target: "255.255.255.255",
		Dial: func(network, _ string) (net.Conn, error) {
			return net.Dial(network, addr)
		},
	}
	if err := sender.Send(context.Background(), mac); err != nil {
		t.Fatalf("Send: %v", err)
	}

	packets := waitForPackets(t, received, 1)
	want, _ := MagicPacket(mac)
	if !bytes.Equal(packets[0], want) {
		t.Fatalf("received packet = % x, want % x", packets[0], want)
	}
}

func TestRouterAPISenderRendersTemplateAndSends(t *testing.T) {
	var gotMethod, gotPath, gotHeader, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotHeader = r.Header.Get("X-Api-Key")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	mac := mustMAC(t, "aa:bb:cc:dd:ee:ff")
	sender := RouterAPISender{
		URL:     srv.URL + "/wol/{{.MAC}}",
		Method:  http.MethodPost,
		Headers: map[string]string{"X-Api-Key": "secret"},
		Body:    `{"mac":"{{.MAC}}"}`,
	}
	if err := sender.Send(context.Background(), mac); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/wol/aa:bb:cc:dd:ee:ff" {
		t.Errorf("path = %q, want /wol/aa:bb:cc:dd:ee:ff", gotPath)
	}
	if gotHeader != "secret" {
		t.Errorf("X-Api-Key header = %q, want secret", gotHeader)
	}
	if !strings.Contains(gotBody, `"mac":"aa:bb:cc:dd:ee:ff"`) {
		t.Errorf("body = %q, want it to contain the rendered MAC", gotBody)
	}
}

func TestRouterAPISenderErrorsOnNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sender := RouterAPISender{URL: srv.URL}
	if err := sender.Send(context.Background(), mustMAC(t, "aa:bb:cc:dd:ee:ff")); err == nil {
		t.Fatal("expected error for 500 response")
	}
}
