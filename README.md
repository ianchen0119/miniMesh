# miniMesh

A minimal Service Mesh implementation in Go, inspired by Istio's Ambient Mesh
model — one proxy per node, no sidecars.

## Architecture

```
┌──────────────────────────── Node A ─────────────────────────────────┐
│                                                                     │
│  ┌─────────┐  iptables   ┌─────────────────────────────────────┐    │
│  │  Pod 1  │─ REDIRECT ─▶│      minimesh-daemon (:15001)       │    │
│  └─────────┘             │                                     │    │
│                          │  originalDst (SO_ORIGINAL_DST)?     │    │
│                          │     │                  │            │    │
│                          │  same-node          remote node     │    │
│                          │     │                  │            │    │
│                          │  ┌──▼──────────┐   ┌───▼─────────┐  │    │
│                          │  │ Unix-socket │   │ TCP mTLS    │──┼──┐ │
│                          │  │ relay (mTLS)│   │ to peer     │  │  │ │
│                          │  │ relay.sock  │   │ :15000      │  │  │ │
│                          │  └──────┬──────┘   └─────────────┘  │  │ │
│  ┌─────────┐             │         │                           │  │ │
│  │  Pod 2  │◀────────────┼─────────┘                           │  │ │
│  └─────────┘             └─────────────────────────────────────┘  │ │
│                                                                   │ │
│  API sock: /run/minimesh/api.sock  (HTTP over Unix socket)        │ │
└───────────────────────────────────────────────────────────────────┘ │
                                                                      │
┌──────────────────────────── Node B ───────────────────────────────┐ │
│              minimesh-daemon  (:15000 mTLS)  ◀──────────────────────┘
└───────────────────────────────────────────────────────────────────┘
```

### Key design decisions

| Requirement | Implementation |
|---|---|
| No sidecar containers | `minimesh-daemon` DaemonSet – one proxy per node (ambient style) |
| Intra-node mTLS | Unix-domain-socket relay wrapped in TLS 1.3 |
| Cross-node mTLS | TCP+mTLS tunnel from local daemon to peer daemon on port `15000` |
| Pod→Service traffic | ClusterIP packets routed through the same proxy; kube-proxy still does load-balancing |
| Traffic interception | iptables `MINIMESH` chain in `nat/PREROUTING` – `REDIRECT` to `:15001` |
| Original destination | Recovered via `SO_ORIGINAL_DST` |
| CLI | `meshctl` talks to the daemon via a Unix-socket HTTP API (no TCP ports) |

## Components

| Binary | Path | Description |
|--------|------|-------------|
| `minimesh-daemon` | `cmd/daemon` | Node proxy agent – deployed as DaemonSet |
| `meshctl` | `cmd/meshctl` | CLI companion |

Internal packages:

| Package | Path | Responsibility |
|---|---|---|
| `daemon` | `pkg/daemon` | Wires cert + iptables + proxy together; serves the Unix-socket API |
| `proxy` | `pkg/proxy` | Transparent proxy + intra-node Unix-socket relay + cross-node mTLS dialer |
| `iptables` | `pkg/iptables` | Manages the `MINIMESH` NAT chain |
| `cert` | `pkg/cert` | In-memory self-signed CA and per-node TLS bundle issuance |

## Quick start

### Build

```bash
make build          # binaries in ./bin/
make docker-build   # Docker image (REGISTRY=minimesh, TAG=latest)
make lint           # go vet ./...
make test           # go test ./...
```

### Deploy on Kubernetes

```bash
kubectl apply -f deploy/daemon.yaml      # ServiceAccount + RBAC + DaemonSet
kubectl apply -f deploy/examples.yaml    # demo workloads
```

For microk8s users there are convenience targets:

```bash
make microk8s-deploy   # save image and import into microk8s
make rollout           # restart + wait for the DaemonSet rollout
```

### Configuration

The daemon reads its pod CIDR and service CIDR from environment variables (or
flags). The defaults match microk8s; override `SVC_CIDR` for other distros
(kubeadm: `10.96.0.0/12`, kind: `10.96.0.0/16`, GKE: `34.118.224.0/20`).

| Env var | Flag | Default | Notes |
|---|---|---|---|
| `NODE_NAME` | `--node-name` | _(downward API)_ | Used for pod CIDR auto-detect |
| `POD_CIDR` | `--pod-cidr` | _(auto-detect from K8s API)_ | Falls back to `node.spec.podCIDR`, then `podCIDRs[0]`, then a `/24` derived from a running pod IP on the node. If all strategies fail, the daemon exits with an error – set `POD_CIDR` explicitly to skip detection. |
| `SVC_CIDR` | `--svc-cidr` | `10.152.183.0/24` (microk8s) | Service ClusterIP range |
| _(none)_ | `--node-addr` | _(empty)_ | External IP of this node, used as source/target for cross-node mTLS |
| _(none)_ | `--node-port` | `15000` | Port for cross-node mTLS tunnels |
| _(none)_ | `--daemon-uid` | `1337` | UID of the daemon process (exempt from iptables redirect) |

Auto-detection of `POD_CIDR` requires `nodes get` and `pods list` RBAC – both
already granted by the ClusterRole in `deploy/daemon.yaml`.

### meshctl commands

`meshctl` connects to `/run/minimesh/api.sock` by default (override with
`--socket`).

```bash
meshctl status     # daemon status (nodeName, podCIDR, svcCIDR, nodeAddr, startTime)
meshctl healthz    # health check
```

## mTLS details

- The daemon generates a self-signed CA in memory on startup and issues itself a
  node certificate (`minimesh.node`).
- TLS 1.3 with mutual authentication is enforced on all relayed connections,
  both for the intra-node Unix-socket relay and the cross-node TCP tunnel.
- Each daemon trusts its own CA only, so all nodes that should talk to each
  other must currently share the same CA material (multi-node CA distribution
  is on the roadmap).

## Ports & sockets

| Endpoint | Bound on | Purpose |
|---|---|---|
| `:15001` (TCP) | host netns | Interception listener (iptables `REDIRECT` target) |
| `:15000` (TCP, mTLS) | host netns | Cross-node tunnel listener |
| `/run/minimesh/relay.sock` (Unix, mTLS) | host filesystem | Intra-node proxy → relay hop |
| `/run/minimesh/api.sock` (Unix, HTTP) | host filesystem | `meshctl` ↔ daemon control plane |

## License

See [LICENSE](./LICENSE).
