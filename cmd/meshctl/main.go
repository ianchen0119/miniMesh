// meshctl is the CLI companion for the miniMesh daemon.
// It communicates with the daemon over a Unix socket HTTP API so no TCP ports
// need to be opened.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/ianchen0119/miniMesh/pkg/daemon"
	"github.com/ianchen0119/miniMesh/pkg/observability"
)

// apiClient returns an *http.Client that connects via the daemon Unix socket.
func apiClient(sockPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
}

func apiGet(sockPath, path string) ([]byte, error) {
	resp, err := apiClient(sockPath).Get("http://minimesh" + path)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w (is daemon running?)", err)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func main() {
	var sockPath string

	root := &cobra.Command{
		Use:   "meshctl",
		Short: "miniMesh CLI – interact with the local daemon",
	}
	root.PersistentFlags().StringVar(&sockPath, "socket",
		daemon.APISockPath, "Path to the daemon API Unix socket")

	// ── status ────────────────────────────────────────────────────────────
	root.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Print daemon status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := apiGet(sockPath, "/status")
			if err != nil {
				return err
			}
			var s map[string]interface{}
			if err := json.Unmarshal(data, &s); err != nil {
				fmt.Print(string(data))
				return nil
			}
			for k, v := range s {
				fmt.Printf("%-15s %v\n", k+":", v)
			}
			return nil
		},
	})

	// ── healthz ───────────────────────────────────────────────────────────
	root.AddCommand(&cobra.Command{
		Use:   "healthz",
		Short: "Check daemon health",
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := apiClient(sockPath).Get("http://minimesh/healthz")
			if err != nil {
				return fmt.Errorf("connect: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				fmt.Println("daemon is healthy")
			} else {
				fmt.Printf("daemon returned %s\n", resp.Status)
			}
			return nil
		},
	})

	// ── observe ───────────────────────────────────────────────────────────
	var pwruArgs []string
	observeCmd := &cobra.Command{
		Use:   "observe",
		Short: "Live packet trace using pwru (requires root and pwru in PATH)",
		Long: `Streams real-time kernel-level packet traces via Cilium's pwru tool.
pwru must be installed separately (https://github.com/cilium/pwru).

Examples:
  meshctl observe -- --filter-dst-ip 10.244.0.5
  meshctl observe -- --filter-src-ip 10.244.0.3 --output-tuple`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Extra positional args after "--" are forwarded to pwru.
			all := append(pwruArgs, args...)
			return observability.PWRU(cmd.Context(), os.Stdout, all...)
		},
	}
	observeCmd.Flags().StringArrayVar(&pwruArgs, "pwru-args", nil,
		"Additional arguments forwarded to pwru")
	root.AddCommand(observeCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
