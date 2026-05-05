// Package iptables manages the miniMesh NAT chain that redirects pod traffic
// to the local daemon proxy.
package iptables

import (
	"fmt"

	ipt "github.com/coreos/go-iptables/iptables"
)

const (
	natTable  = "nat"
	meshChain = "MINIMESH"
	// ProxyPort is where the daemon's interception listener binds.
	ProxyPort = "15001"
	// NodePort is where the daemon listens for cross-node mTLS tunnels.
	NodePort = "15000"
)

// Manager manages iptables rules for the mesh.
type Manager struct {
	t *ipt.IPTables
}

// New returns a new iptables Manager.
func New() (*Manager, error) {
	t, err := ipt.New()
	if err != nil {
		return nil, err
	}
	return &Manager{t: t}, nil
}

// Setup installs mesh NAT rules for the given pod CIDR.
// daemonUID is the UID of the daemon process; its outbound traffic is skipped
// to prevent infinite redirect loops.
func (m *Manager) Setup(podCIDR, daemonUID string) error {
	// Create mesh chain (ignore "already exists" error).
	_ = m.t.NewChain(natTable, meshChain)

	rules := [][]string{
		// Skip loopback traffic.
		{"-o", "lo", "-j", "RETURN"},
		// Skip traffic originating from the daemon itself.
		{"-m", "owner", "--uid-owner", daemonUID, "-j", "RETURN"},
		// Redirect pod-to-pod TCP to the proxy port.
		{"-p", "tcp", "-d", podCIDR, "-j", "REDIRECT", "--to-port", ProxyPort},
	}
	for _, r := range rules {
		if err := m.t.AppendUnique(natTable, meshChain, r...); err != nil {
			return fmt.Errorf("append rule %v: %w", r, err)
		}
	}
	if err := m.t.AppendUnique(natTable, "OUTPUT", "-j", meshChain); err != nil {
		return fmt.Errorf("hook OUTPUT chain: %w", err)
	}
	return nil
}

// Teardown removes all mesh iptables rules and deletes the mesh chain.
func (m *Manager) Teardown() {
	_ = m.t.Delete(natTable, "OUTPUT", "-j", meshChain)
	_ = m.t.ClearChain(natTable, meshChain)
	_ = m.t.DeleteChain(natTable, meshChain)
}
