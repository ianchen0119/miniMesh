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
// daemonUID is unused but kept for API compatibility.
//
// Only PREROUTING is hooked: pod packets leave their netns via veth and enter
// the host in PREROUTING. The source CIDR filter (-s podCIDR) ensures only
// pod-originated traffic is redirected; kubelet/node traffic is left alone.
// OUTPUT is intentionally NOT hooked to avoid intercepting host system traffic
// (kubelet health checks, Prometheus scraping, etc.).
func (m *Manager) Setup(podCIDR, _ string) error {
	// Always start from a clean slate to flush rules from a previous instance.
	_ = m.t.NewChain(natTable, meshChain)
	_ = m.t.ClearChain(natTable, meshChain)
	// Skip loopback.
	if err := m.t.AppendUnique(natTable, meshChain, "-i", "lo", "-j", "RETURN"); err != nil {
		return fmt.Errorf("append loopback rule: %w", err)
	}
	// Skip mesh infrastructure ports to avoid redirect loops.
	for _, port := range []string{ProxyPort, NodePort} {
		if err := m.t.AppendUnique(natTable, meshChain,
			"-p", "tcp", "--dport", port, "-j", "RETURN",
		); err != nil {
			return fmt.Errorf("append skip-port %s rule: %w", port, err)
		}
	}
	// Intercept ALL pod-originated TCP: -s podCIDR catches pod→pod,
	// pod→Service ClusterIP, and pod→external. The proxy decides per-flow
	// whether to apply mTLS (intra-cluster) or plain passthrough (external).
	//
	// Rationale: kube-proxy on microk8s uses the iptables-legacy backend while
	// we install rules in nft. The two backends are independent netfilter rule
	// sets, and nft runs before legacy here, so by the time we see the packet
	// dst is still the ClusterIP (NOT in podCIDR). Filtering by `-d podCIDR`
	// would let pod→Service traffic skip the mesh entirely.
	//
	// We use DNAT with an explicit destination (127.0.0.1:ProxyPort) rather than
	// REDIRECT. REDIRECT resolves the target address from the incoming interface's
	// primary IP; Calico's veth interfaces (cali*) have NO host-side IP, so the
	// kernel returns NF_DROP when REDIRECT is used on them. DNAT with an explicit
	// address avoids that lookup entirely.
	if err := m.t.AppendUnique(natTable, meshChain,
		"-p", "tcp", "-s", podCIDR,
		"-j", "DNAT", "--to-destination", "127.0.0.1:"+ProxyPort,
	); err != nil {
		return fmt.Errorf("append dnat rule: %w", err)
	}
	// Hook PREROUTING at position 1 so our chain runs BEFORE KUBE-SERVICES.
	// kube-proxy installs KUBE-SERVICES near the top of PREROUTING; if it runs
	// first it DNATs Service ClusterIPs to pod IPs and the conntrack entry is
	// finalised, so any later NAT rules (ours) become a no-op for that flow.
	// Inserting at the head guarantees we see Service traffic with its
	// original ClusterIP destination.
	//
	// Idempotency: remove any stale jump first, then insert at position 1.
	_ = m.t.Delete(natTable, "PREROUTING", "-j", meshChain)
	if err := m.t.Insert(natTable, "PREROUTING", 1, "-j", meshChain); err != nil {
		return fmt.Errorf("hook PREROUTING chain: %w", err)
	}
	return nil
}

// Teardown removes all mesh iptables rules and deletes the mesh chain.
func (m *Manager) Teardown() {
	_ = m.t.Delete(natTable, "PREROUTING", "-j", meshChain)
	_ = m.t.ClearChain(natTable, meshChain)
	_ = m.t.DeleteChain(natTable, meshChain)
}
