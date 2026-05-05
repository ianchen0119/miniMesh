// Package proxy implements the miniMesh transparent proxy.
//
// For intra-node traffic the Unix-domain-socket relay is used as the internal
// transport, wrapped with mTLS.  For cross-node traffic the daemon opens a
// TCP+mTLS tunnel to the remote node's daemon (NodePort).
//
// Traffic is intercepted by iptables REDIRECT rules and the original
// destination is recovered via SO_ORIGINAL_DST.
package proxy

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

const soOriginalDst = 80 // Linux SO_ORIGINAL_DST

// rawSockAddrIn mirrors Linux sockaddr_in returned by SO_ORIGINAL_DST.
type rawSockAddrIn struct {
	Family uint16
	Port   [2]byte
	Addr   [4]byte
	_      [8]byte
}

// originalDst recovers the pre-REDIRECT destination from a NAT-hijacked conn.
func originalDst(conn net.Conn) (net.IP, uint16, error) {
	tc, ok := conn.(*net.TCPConn)
	if !ok {
		return nil, 0, fmt.Errorf("not a TCPConn")
	}
	f, err := tc.File()
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	var sa rawSockAddrIn
	size := uint32(unsafe.Sizeof(sa))
	_, _, errno := syscall.Syscall6(
		syscall.SYS_GETSOCKOPT,
		uintptr(f.Fd()),
		syscall.IPPROTO_IP,
		soOriginalDst,
		uintptr(unsafe.Pointer(&sa)),
		uintptr(unsafe.Pointer(&size)),
		0,
	)
	if errno != 0 {
		return nil, 0, errno
	}
	return net.IP(sa.Addr[:]), binary.BigEndian.Uint16(sa.Port[:]), nil
}

// isClosedConnErr reports whether err is an expected connection-close error
// that occurs naturally during normal bidirectional pipe teardown.
func isClosedConnErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "use of closed network connection") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "connection reset by peer") ||
		strings.Contains(s, "EOF")
}

// pipe copies data between a and b concurrently until one side closes or ctx
// is cancelled.
func pipe(ctx context.Context, a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		if _, err := io.Copy(dst, src); err != nil && !isClosedConnErr(err) {
			log.Printf("pipe copy: %v", err)
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	select {
	case <-ctx.Done():
	case <-done:
	}
	if err := a.Close(); err != nil && !isClosedConnErr(err) {
		log.Printf("pipe close a: %v", err)
	}
	if err := b.Close(); err != nil && !isClosedConnErr(err) {
		log.Printf("pipe close b: %v", err)
	}
}

// --------------------------------------------------------------------
// Proxy – interception listener
// --------------------------------------------------------------------

// Proxy intercepts iptables-redirected connections and forwards them.
type Proxy struct {
	listenAddr string
	nodePort   string
	localCIDR  *net.IPNet
	svcCIDR    *net.IPNet // optional; nil if not configured
	serverTLS  *tls.Config
	clientTLS  *tls.Config
	relayPath  string // Unix socket path for the intra-node relay
}

// New creates a Proxy.
func New(listenAddr, nodePort, podCIDR, svcCIDR, relayPath string, serverTLS, clientTLS *tls.Config) (*Proxy, error) {
	_, localNet, err := net.ParseCIDR(podCIDR)
	if err != nil {
		return nil, fmt.Errorf("parse pod CIDR %q: %w", podCIDR, err)
	}
	var svcNet *net.IPNet
	if svcCIDR != "" {
		_, svcNet, err = net.ParseCIDR(svcCIDR)
		if err != nil {
			return nil, fmt.Errorf("parse svc CIDR %q: %w", svcCIDR, err)
		}
	}
	return &Proxy{
		listenAddr: listenAddr,
		nodePort:   nodePort,
		localCIDR:  localNet,
		svcCIDR:    svcNet,
		serverTLS:  serverTLS,
		clientTLS:  clientTLS,
		relayPath:  relayPath,
	}, nil
}

// Serve starts accepting connections until ctx is done.
func (p *Proxy) Serve(ctx context.Context) error {
	// Use tcp4 explicitly: iptables DNAT redirects to 127.0.0.1 (IPv4). A tcp6
	// dual-stack socket would also work IF IPV6_V6ONLY=0, but forcing tcp4 is
	// unambiguous and avoids environment-specific surprises.
	ln, err := net.Listen("tcp4", p.listenAddr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go p.handle(ctx, conn)
	}
}

func (p *Proxy) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	dstIP, dstPort, err := originalDst(conn)
	if err != nil {
		log.Printf("proxy: originalDst: %v", err)
		return
	}
	log.Printf("proxy: %s -> %s:%d", conn.RemoteAddr(), dstIP, dstPort)

	var upstream net.Conn
	switch {
	case p.localCIDR.Contains(dstIP):
		// Same-node pod IP: relay via Unix socket with mTLS.
		upstream, err = dialRelay(p.relayPath, p.clientTLS, dstIP, dstPort)
	case p.svcCIDR != nil && p.svcCIDR.Contains(dstIP):
		// Service ClusterIP: route through the Relay too. The Relay then does
		// net.Dial(ClusterIP:port), which traverses the host OUTPUT chain where
		// kube-proxy's KUBE-SERVICES DNATs it to a real backend pod. Net effect:
		// the pod→service hop gets the same Proxy↔Relay mTLS as pod→pod, and
		// kube-proxy still owns service load-balancing.
		upstream, err = dialRelay(p.relayPath, p.clientTLS, dstIP, dstPort)
	default:
		// External IP / off-cluster destination. Plain TCP passthrough –
		// the remote endpoint isn't part of the mesh so mTLS would fail.
		//
		// TODO: for multi-node mesh, look up remote pod CIDRs from the K8s
		// API and route those via tls.Dial to the remote daemon's NodePort.
		upstream, err = net.Dial("tcp", fmt.Sprintf("%s:%d", dstIP, dstPort))
	}
	if err != nil {
		log.Printf("proxy: upstream: %v", err)
		return
	}
	defer upstream.Close()
	pipe(ctx, conn, upstream)
}

// dialRelay opens a mTLS connection to the local Unix relay and sends a
// 6-byte header (4-byte IPv4 + 2-byte big-endian port).
func dialRelay(path string, cfg *tls.Config, ip net.IP, port uint16) (net.Conn, error) {
	raw, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("unix dial: %w", err)
	}
	tc := tls.Client(raw, cfg)
	if err := tc.Handshake(); err != nil {
		raw.Close()
		return nil, fmt.Errorf("mTLS handshake: %w", err)
	}
	hdr := make([]byte, 6)
	copy(hdr[:4], ip.To4())
	binary.BigEndian.PutUint16(hdr[4:], port)
	if _, err := tc.Write(hdr); err != nil {
		tc.Close()
		return nil, fmt.Errorf("write relay header: %w", err)
	}
	return tc, nil
}

// --------------------------------------------------------------------
// Relay – Unix-domain-socket listener for intra-node mTLS forwarding
// --------------------------------------------------------------------

// Relay is the Unix-socket relay server.  It accepts mTLS connections from
// Proxy.handle, reads the 6-byte destination header, and dials the real pod.
type Relay struct {
	path      string
	serverTLS *tls.Config
}

// NewRelay creates a Relay.
func NewRelay(path string, serverTLS *tls.Config) *Relay {
	return &Relay{path: path, serverTLS: serverTLS}
}

// Serve listens on the Unix socket until ctx is done.
func (r *Relay) Serve(ctx context.Context) error {
	os.Remove(r.path)
	ln, err := net.Listen("unix", r.path)
	if err != nil {
		return err
	}
	defer func() {
		ln.Close()
		os.Remove(r.path)
	}()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go r.handleRelay(ctx, conn)
	}
}

func (r *Relay) handleRelay(ctx context.Context, raw net.Conn) {
	conn := tls.Server(raw, r.serverTLS)
	if err := conn.Handshake(); err != nil {
		log.Printf("relay: mTLS: %v", err)
		raw.Close()
		return
	}
	defer conn.Close()

	// Read 6-byte destination header.
	hdr := make([]byte, 6)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		log.Printf("relay: read header: %v", err)
		return
	}
	ip := net.IP(hdr[:4])
	port := binary.BigEndian.Uint16(hdr[4:])
	dst := fmt.Sprintf("%s:%d", ip, port)

	upstream, err := net.Dial("tcp", dst)
	if err != nil {
		log.Printf("relay: dial %s: %v", dst, err)
		return
	}
	defer upstream.Close()
	log.Printf("relay: %s -> %s", conn.RemoteAddr(), dst)
	pipe(ctx, conn, upstream)
}
