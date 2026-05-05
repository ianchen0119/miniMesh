// minimesh-daemon is the per-node miniMesh proxy agent.
// It is intended to be deployed as a Kubernetes DaemonSet.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/ianchen0119/miniMesh/pkg/daemon"
)

// lookupPodCIDR queries the Kubernetes API for the pod CIDR of the given node.
// It tries three strategies in order:
//  1. node.spec.podCIDR
//  2. node.spec.podCIDRs[0]  (dual-stack clusters)
//  3. Derive a /24 from the IPs of pods already running on the node
//
// It relies on in-cluster config and requires "nodes get" + "pods list" RBAC.
func lookupPodCIDR(ctx context.Context, nodeName string) (string, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return "", fmt.Errorf("in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("k8s client: %w", err)
	}

	node, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get node %q: %w", nodeName, err)
	}

	// Strategy 1: spec.podCIDR
	if node.Spec.PodCIDR != "" {
		return node.Spec.PodCIDR, nil
	}

	// Strategy 2: spec.podCIDRs (dual-stack / some CNIs)
	if len(node.Spec.PodCIDRs) > 0 {
		return node.Spec.PodCIDRs[0], nil
	}

	// Strategy 3: derive /24 from a running pod's IP on this node.
	// Some CNIs (e.g. Calico in microk8s) never populate spec.podCIDR.
	pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		return "", fmt.Errorf("list pods on node %q: %w", nodeName, err)
	}
	for _, pod := range pods.Items {
		if pod.Status.PodIP == "" {
			continue
		}
		ip := net.ParseIP(pod.Status.PodIP)
		if ip == nil || ip.To4() == nil {
			continue
		}
		// Mask to /24 – covers virtually all per-node CNI allocations.
		masked := ip.Mask(net.CIDRMask(24, 32))
		cidr := fmt.Sprintf("%s/24", masked)
		log.Printf("pod CIDR derived from pod %s/%s IP %s -> %s",
			pod.Namespace, pod.Name, pod.Status.PodIP, cidr)
		return cidr, nil
	}

	return "", fmt.Errorf(
		"node %q has no podCIDR and no pods with IPs; use --pod-cidr to set it explicitly",
		nodeName,
	)
}

func main() {
	cfg := daemon.Config{}

	cmd := &cobra.Command{
		Use:   "minimesh-daemon",
		Short: "miniMesh node proxy agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signal.NotifyContext(
				context.Background(), os.Interrupt, syscall.SIGTERM,
			)
			defer cancel()

			// POD_CIDR env wins over auto-detection. The CLI flag still wins
			// over the env var (Cobra applies flag values before RunE runs).
			if cfg.PodCIDR == "" {
				if env := os.Getenv("POD_CIDR"); env != "" {
					cfg.PodCIDR = env
					log.Printf("pod CIDR from $POD_CIDR: %s", env)
				}
			}
			if cfg.PodCIDR == "" {
				cidr, err := lookupPodCIDR(ctx, cfg.NodeName)
				if err != nil {
					return fmt.Errorf("auto-detect pod CIDR: %w", err)
				}
				cfg.PodCIDR = cidr
				log.Printf("pod CIDR auto-detected from node: %s", cidr)
			}

			d, err := daemon.New(cfg)
			if err != nil {
				return err
			}
			log.Printf("miniMesh daemon starting (node=%s podCIDR=%s svcCIDR=%s)",
				cfg.NodeName, cfg.PodCIDR, cfg.SvcCIDR)
			return d.Run(ctx)
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.NodeName, "node-name",
		os.Getenv("NODE_NAME"), "Kubernetes node name (or $NODE_NAME)")
	f.StringVar(&cfg.PodCIDR, "pod-cidr",
		"", "Pod CIDR allocated to this node (overrides $POD_CIDR; auto-detected if both empty)")
	// microk8s default Service CIDR is 10.152.183.0/24. Other distros differ
	// (kubeadm: 10.96.0.0/12, kind: 10.96.0.0/16). Override via $SVC_CIDR or --svc-cidr.
	defaultSvcCIDR := os.Getenv("SVC_CIDR")
	if defaultSvcCIDR == "" {
		defaultSvcCIDR = "10.152.183.0/24"
	}
	f.StringVar(&cfg.SvcCIDR, "svc-cidr",
		defaultSvcCIDR, "Kubernetes Service ClusterIP CIDR (or $SVC_CIDR)")
	f.StringVar(&cfg.NodeAddr, "node-addr",
		"", "External IP of this node (used for cross-node mTLS)")
	f.StringVar(&cfg.NodePort, "node-port",
		"15000", "Port for cross-node mTLS tunnels")
	f.StringVar(&cfg.DaemonUID, "daemon-uid",
		"1337", "UID of the daemon process (exempt from iptables redirect)")

	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
