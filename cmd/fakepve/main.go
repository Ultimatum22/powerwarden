// Command fakepve is a development stand-in for the Proxmox host, so
// labpower can be run and clicked through locally (`make dev`) without a
// real Proxmox, router, or notification service. It is never part of a
// release build.
//
// It serves:
//   - the Proxmox API subset labpower uses, over TLS with a self-signed
//     certificate kept in -dir (so the pinned fingerprint stays stable);
//   - a UDP Wake-on-LAN listener that "boots" the host on a magic packet;
//   - plain HTTP on loopback: an ntfy sink that logs notifications, and
//     /control endpoints to simulate events outside labpower.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fakepve:", err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", ".dev", "directory for the TLS certificate and fingerprint file")
	apiAddr := flag.String("api", "127.0.0.1:8006", "Proxmox API (TLS) listen address")
	sideAddr := flag.String("side", "127.0.0.1:8007", "ntfy sink + control (plain HTTP) listen address")
	wolAddr := flag.String("wol", "127.0.0.1:40009", "Wake-on-LAN UDP listen address")
	node := flag.String("node", "pve01", "node name")
	macStr := flag.String("mac", "aa:bb:cc:dd:ee:ff", "host NIC MAC that Wake-on-LAN must target")
	tokenID := flag.String("token-id", "", "expected API token ID (empty: accept any token)")
	tokenFile := flag.String("token-file", "", "file holding the expected API token secret")
	verbose := flag.Bool("v", false, "log every API request")
	flag.Parse()

	for _, addr := range []string{*apiAddr, *sideAddr, *wolAddr} {
		if err := requireLoopback(addr); err != nil {
			return err
		}
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})).With("app", "fakepve")

	mac, err := net.ParseMAC(*macStr)
	if err != nil {
		return fmt.Errorf("-mac: %w", err)
	}
	var authHeader string
	if *tokenID != "" {
		secret, err := os.ReadFile(*tokenFile)
		if err != nil {
			return fmt.Errorf("-token-file: %w", err)
		}
		authHeader = "PVEAPIToken=" + *tokenID + "=" + strings.TrimSpace(string(secret))
	}

	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	cert, err := loadOrCreateCert(*dir)
	if err != nil {
		return err
	}
	fp := fingerprint(cert.Certificate[0])
	if err := os.WriteFile(filepath.Join(*dir, "fingerprint"), []byte(fp+"\n"), 0o600); err != nil {
		return err
	}

	pve := NewPVE(*node, authHeader, mac, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	api := &http.Server{
		Addr:              *apiAddr,
		Handler:           pve.APIHandler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelDebug), // aborted "host off" requests are expected
	}
	side := &http.Server{Addr: *sideAddr, Handler: pve.SideHandler(), ReadHeaderTimeout: 5 * time.Second}

	udp, err := net.ListenPacket("udp", *wolAddr)
	if err != nil {
		return fmt.Errorf("listen wol: %w", err)
	}
	defer udp.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, _, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			pve.HandleWoL(buf[:n])
		}
	}()

	errs := make(chan error, 2)
	go func() { errs <- api.ListenAndServeTLS("", "") }()
	go func() { errs <- side.ListenAndServe() }()

	logger.Info("fake Proxmox ready",
		"api", "https://"+*apiAddr, "fingerprint", fp,
		"wol", "udp://"+*wolAddr, "side", "http://"+*sideAddr)

	select {
	case <-ctx.Done():
	case err := <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = api.Shutdown(shutdownCtx)
	_ = side.Shutdown(shutdownCtx)
	return nil
}

// requireLoopback keeps the fake (unauthenticated controls, a fake API)
// off the network.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("address %q must be loopback", addr)
	}
	return nil
}

// fingerprint formats a certificate's SHA-256 the way labpower's
// proxmox.tls_fingerprint expects ("AB:CD:...").
func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// loadOrCreateCert reuses dir's certificate, or creates a self-signed one
// for 127.0.0.1/localhost, like a fresh Proxmox install has.
func loadOrCreateCert(dir string) (tls.Certificate, error) {
	certPath, keyPath := filepath.Join(dir, "fakepve.crt"), filepath.Join(dir, "fakepve.key")
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return cert, nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "fakepve"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}
