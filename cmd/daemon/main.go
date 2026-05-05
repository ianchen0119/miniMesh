// minimesh-daemon is the per-node miniMesh proxy agent.
// It is intended to be deployed as a Kubernetes DaemonSet.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ianchen0119/miniMesh/pkg/daemon"
)

func main() {
	cfg := daemon.Config{}

	cmd := &cobra.Command{
		Use:   "minimesh-daemon",
		Short: "miniMesh node proxy agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := daemon.New(cfg)
			if err != nil {
				return err
			}
			ctx, cancel := signal.NotifyContext(
				context.Background(), os.Interrupt, syscall.SIGTERM,
			)
			defer cancel()
			log.Printf("miniMesh daemon starting (node=%s podCIDR=%s)",
				cfg.NodeName, cfg.PodCIDR)
			return d.Run(ctx)
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.NodeName, "node-name",
		os.Getenv("NODE_NAME"), "Kubernetes node name (or $NODE_NAME)")
	f.StringVar(&cfg.PodCIDR, "pod-cidr",
		"10.244.0.0/24", "Pod CIDR allocated to this node")
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
