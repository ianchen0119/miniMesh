// Package daemon wires together the cert, iptables, and proxy subsystems into
// the miniMesh node agent.  It also exposes a lightweight HTTP API over a Unix
// socket so that meshctl can query live status without opening any TCP ports.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ianchen0119/miniMesh/pkg/cert"
	"github.com/ianchen0119/miniMesh/pkg/iptables"
	"github.com/ianchen0119/miniMesh/pkg/proxy"
)

const (
	// APISockPath is the Unix socket used by meshctl to talk to the daemon.
	APISockPath = "/run/minimesh/api.sock"
	relaySock   = "/run/minimesh/relay.sock"
)

// Config holds the daemon configuration (typically supplied via CLI flags or
// environment variables injected by a Kubernetes DaemonSet).
type Config struct {
	NodeName  string
	PodCIDR   string
	NodeAddr  string
	NodePort  string
	DaemonUID string // UID of daemon process (skipped in iptables)
}

// Status is returned by GET /status.
type Status struct {
	NodeName  string    `json:"nodeName"`
	StartTime time.Time `json:"startTime"`
	PodCIDR   string    `json:"podCIDR"`
	NodeAddr  string    `json:"nodeAddr"`
}

// Daemon is the miniMesh node agent.
type Daemon struct {
	cfg       Config
	ipt       *iptables.Manager
	prx       *proxy.Proxy
	relay     *proxy.Relay
	status    Status
	apiServer *http.Server
	mu        sync.RWMutex
}

// New initialises all subsystems and returns a ready-to-run Daemon.
func New(cfg Config) (*Daemon, error) {
	if err := os.MkdirAll("/run/minimesh", 0o700); err != nil {
		return nil, fmt.Errorf("mkdir /run/minimesh: %w", err)
	}

	ca, err := cert.NewCA()
	if err != nil {
		return nil, fmt.Errorf("new CA: %w", err)
	}
	bundle, err := ca.Issue("minimesh.node")
	if err != nil {
		return nil, fmt.Errorf("issue node cert: %w", err)
	}
	serverTLS := ca.ServerTLS(bundle)
	clientTLS := ca.ClientTLS(bundle, "minimesh.node")

	prx, err := proxy.New(
		":"+iptables.ProxyPort,
		cfg.NodePort,
		cfg.PodCIDR,
		relaySock,
		serverTLS,
		clientTLS,
	)
	if err != nil {
		return nil, fmt.Errorf("new proxy: %w", err)
	}

	ipm, err := iptables.New()
	if err != nil {
		return nil, fmt.Errorf("new iptables manager: %w", err)
	}

	d := &Daemon{
		cfg:   cfg,
		ipt:   ipm,
		prx:   prx,
		relay: proxy.NewRelay(relaySock, serverTLS),
		status: Status{
			NodeName:  cfg.NodeName,
			StartTime: time.Now(),
			PodCIDR:   cfg.PodCIDR,
			NodeAddr:  cfg.NodeAddr,
		},
	}
	d.setupAPI()
	return d, nil
}

func (d *Daemon) setupAPI() {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		d.mu.RLock()
		s := d.status
		d.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(s); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	d.apiServer = &http.Server{Handler: mux}
}

// Run starts the daemon and blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.ipt.Setup(d.cfg.PodCIDR, d.cfg.DaemonUID); err != nil {
		log.Printf("iptables setup: %v (running without traffic interception)", err)
	} else {
		defer d.ipt.Teardown()
	}

	os.Remove(APISockPath)
	apiLn, err := net.Listen("unix", APISockPath)
	if err != nil {
		return fmt.Errorf("api listen: %w", err)
	}

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.apiServer.Serve(apiLn); err != nil && err != http.ErrServerClosed {
			log.Printf("api server: %v", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.relay.Serve(ctx); err != nil {
			log.Printf("relay: %v", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.prx.Serve(ctx); err != nil {
			log.Printf("proxy: %v", err)
		}
	}()

	<-ctx.Done()
	if err := d.apiServer.Shutdown(context.Background()); err != nil {
		log.Printf("api shutdown: %v", err)
	}
	os.Remove(APISockPath)
	wg.Wait()
	return nil
}
