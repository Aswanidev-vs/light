package light

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const (
	quicProbeTimeout  = 1500 * time.Millisecond
	quicProbeCacheTTL = 60 * time.Second
)

// lightQUICConfig returns the QUIC connection options used by both the HTTP/3
// server and the client. quic-go's defaults (512 KiB initial / 6 MiB max
// stream window, 30 s idle timeout) are sized for the public internet: on a
// LAN they add flow-control round trips during the ramp-up of the four
// parallel uploads and drop a connection that sits paused for over half a
// minute.
func lightQUICConfig() *quic.Config {
	return &quic.Config{
		InitialStreamReceiveWindow:     2 << 20,  // 2 MiB
		MaxStreamReceiveWindow:         16 << 20, // 16 MiB
		InitialConnectionReceiveWindow: 8 << 20,  // 8 MiB
		MaxConnectionReceiveWindow:     64 << 20, // 64 MiB
		MaxIdleTimeout:                 15 * time.Minute,
	}
}

// quicHTTP3Server is kept as a small interface so the transfer service does
// not depend on HTTP/3 implementation details outside this file.
type quicHTTP3Server interface {
	Serve(net.PacketConn) error
	Close() error
}

func newQUICServer(addr string, handler http.Handler) (quicHTTP3Server, error) {
	certificate, err := newQUICCertificate()
	if err != nil {
		return nil, err
	}
	return &http3.Server{
		Addr:       addr,
		Handler:    handler,
		QUICConfig: lightQUICConfig(),
		TLSConfig: http3.ConfigureTLSConfig(&tls.Config{
			Certificates: []tls.Certificate{certificate},
		}),
	}, nil
}

func newQUICCertificate() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"light.local", "localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}

func newQUICClient() (*http.Client, func(), error) {
	transport := &http3.Transport{
		// Light currently has no certificate exchange/identity trust store. The
		// QUIC path is opt-in and retains the app's existing LAN trust model.
		// Traffic is encrypted, but peer authentication is not provided yet.
		TLSClientConfig:    &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // experimental LAN transport
		DisableCompression: true,
		QUICConfig:         lightQUICConfig(),
	}
	return &http.Client{Transport: transport}, func() { _ = transport.Close() }, nil
}

// quicProbeState records the outcome of a peer's HTTP/3 capability probe.
type quicProbeState struct {
	at time.Time
	ok bool
}

func (s *FileTransferService) clientForPeer(peerAddr string) (*http.Client, string, func(), error) {
	tcpClient := s.sharedTCPClient()
	if normalizeTransportMode(s.settings.GetSettings().TransportMode) != "quic" {
		return tcpClient, "http", func() {}, nil
	}

	// Reuse the last probe result briefly: re-probing on every send batch adds
	// up to quicProbeTimeout of latency for peers on the stable TCP setting
	// (the common case while in QUIC mode).
	s.mu.Lock()
	cached, ok := s.quicProbes[peerAddr]
	s.mu.Unlock()
	if ok && time.Since(cached.at) < quicProbeCacheTTL {
		if cached.ok {
			client, err := s.sharedQUICClient()
			if err != nil {
				return nil, "", func() {}, err
			}
			return client, "https", func() {}, nil
		}
		return tcpClient, "http", func() {}, nil
	}

	client, err := s.sharedQUICClient()
	if err != nil {
		return nil, "", func() {}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), quicProbeTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+peerAddr+"/api/capabilities", nil)
	if err == nil {
		var resp *http.Response
		resp, err = client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
	}
	cancel()

	s.mu.Lock()
	s.quicProbes[peerAddr] = quicProbeState{at: time.Now(), ok: err == nil}
	s.mu.Unlock()

	if err == nil {
		return client, "https", func() {}, nil
	}
	// A QUIC handshake failure is expected when the peer is on the stable TCP
	// setting or the network blocks UDP. Fall back before /api/prepare and any
	// file body are sent, so the transfer cannot be duplicated or half-retried.
	return tcpClient, "http", func() {}, nil
}

func (s *FileTransferService) sharedTCPClient() *http.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tcpClient == nil {
		s.tcpClient = newTCPClient()
	}
	return s.tcpClient
}

func (s *FileTransferService) sharedQUICClient() (*http.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quicClient != nil {
		return s.quicClient, nil
	}
	client, closeClient, err := newQUICClient()
	if err != nil {
		return nil, err
	}
	s.quicClient = client
	s.quicClose = closeClient
	return client, nil
}
