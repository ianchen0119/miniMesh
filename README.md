# MiniMesh

A minimal Service Mesh implementation in Go (~600 lines), inspired by Istio's Ambient Mesh model.

## Architecture

```
┌─────────────────────────── Node ───────────────────────────────────┐
│                                                                     │
│  ┌─────────┐  TCP   ┌─────────────────────────────────────────┐    │
│  │  Pod A  │───────▶│        minimesh-daemon (:15001)          │    │
│  └─────────┘        │                                           │    │
│                     │  originalDst? podCIDR / svcCIDR / other? │    │
│                     │       │                   │               │    │
│                     │  ┌────▼──────────┐  ┌────▼────────────┐  │    │
│                     │  │ Unix socket   │  │ TCP mTLS        │  │    │
│                     │  │ relay (mTLS)  │  │ to remote node  │  │    │
│                     │  │ relay.sock    │  │ :15000 (TODO)   │  │    │
│                     │  └──────┬────────┘  └─────────────────┘  │    │
│  ┌─────────┐  TCP   │         │                                 │    │
│  │  Pod B  │◀───────┼─────────┘                                 │    │
│  └─────────┘        │                                           │    │
│                     └─────────────────────────────────────────┘    │
│  API sock: /run/minimesh/api.sock  (HTTP over Unix socket)          │
└─────────────────────────────────────────────────────────────────────┘
```

### Key design decisions

| Requirement | Implementation |
|---|---|
| No sidecar containers | `minimesh-daemon` DaemonSet – one proxy per node (ambient style) |
| Pod→Pod mTLS | Unix-domain-socket relay wrapped in TLS 1.3 |
| Pod→Service mTLS | ClusterIP traffic also routed through the relay; kube-proxy still does load-balancing |
| Traffic interception | iptables `MINIMESH` chain in `nat/PREROUTING` (DNAT to 127.0.0.1:15001) |
| CLI | `meshctl` talks to the daemon via a Unix-socket HTTP API |

## Components

| Binary | Path | Description |
|--------|------|-------------|
| `minimesh-daemon` | `cmd/daemon` | Node proxy agent – deployed as DaemonSet |
| `meshctl` | `cmd/meshctl` | CLI companion |

## Quick start

### Build

```bash
make build          # binaries in ./bin/
make docker-build   # Docker images
```

### Deploy on Kubernetes

```bash
kubectl apply -f deploy/daemon.yaml      # daemon DaemonSet
kubectl apply -f deploy/examples.yaml    # example workloads
```

### Configuration

The daemon reads its pod CIDR and service CIDR from environment variables (or
flags). The defaults match microk8s; override `SVC_CIDR` for other distros
(kubeadm: `10.96.0.0/12`, kind: `10.96.0.0/16`, GKE: `34.118.224.0/20`).

| Env var | Flag | Default | Notes |
|---|---|---|---|
| `POD_CIDR` | `--pod-cidr` | _(auto-detect from K8s API)_ | Set explicitly to skip API lookup |
| `SVC_CIDR` | `--svc-cidr` | `10.152.183.0/24` (microk8s) | Service ClusterIP range |
| `NODE_NAME` | `--node-name` | _(downward API)_ | Used for pod CIDR auto-detect |

### meshctl commands

```bash
meshctl status     # daemon status (node, podCIDR, svcCIDR, startTime)
meshctl healthz    # health check
```

## mTLS details

- The daemon generates a self-signed CA in memory on startup.
- TLS 1.3 with mutual authentication is enforced on all relayed connections.
- mTLS protects the **Proxy ↔ Relay** hop (both endpoints inside the daemon).
  Pod→Pod and Pod→Service share the same mTLS path; cross-node end-to-end mTLS
  is on the roadmap (requires Service discovery via EndpointSlice + a remote
  daemon NodePort tunnel).
# miniMesh

A minimal Service Mesh implementation in Go (~1 000 lines of code), inspired by Istio's Ambient Mesh model.

## Architecture

```
┌─────────────────────────── Node ───────────────────────────────────┐
│                                                                     │
│  ┌─────────┐  TCP   ┌─────────────────────────────────────────┐    │
│  │  Pod A  │───────▶│        minimesh-daemon (:15001)          │    │
│  └─────────┘        │                                           │    │
│                     │  originalDst? same-node or remote?       │    │
│                     │       │                   │               │    │
│                     │  ┌────▼──────────┐  ┌────▼────────────┐  │    │
│                     │  │ Unix socket   │  │ TCP mTLS        │  │    │
│                     │  │ relay (mTLS)  │  │ to remote node  │  │    │
│                     │  │ relay.sock    │  │ :15000          │  │    │
│                     │  └──────┬────────┘  └─────────────────┘  │    │
│  ┌─────────┐  TCP   │         │                                 │    │
│  │  Pod B  │◀───────┼─────────┘                                 │    │
│  └─────────┘        │                                           │    │
│                     └─────────────────────────────────────────┘    │
│  API sock: /run/minimesh/api.sock  (HTTP over Unix socket)          │
└─────────────────────────────────────────────────────────────────────┘
```

### Key design decisions

| Requirement | Implementation |
|---|---|
| No sidecar containers | `minimesh-daemon` DaemonSet – one proxy per node (ambient style) |
| Intra-node mTLS | Unix-domain-socket relay wrapped in TLS 1.3 |
| Node-to-node traffic | iptables REDIRECT + TCP mTLS tunnel to remote daemon |
| Observability | `meshctl observe` delegates to [Cilium pwru](https://github.com/cilium/pwru) |
| K8s CRDs | `MeshPolicy` (mTLS mode) · `MeshCertificate` (cert lifecycle) |
| CLI | `meshctl` communicates with daemon via Unix-socket HTTP API |

## Components

| Binary | Path | Description |
|--------|------|-------------|
| `minimesh-daemon` | `cmd/daemon` | Node proxy agent – deployed as DaemonSet |
| `meshctl` | `cmd/meshctl` | CLI companion |
| `minimesh-operator` | `cmd/operator` | Kubernetes controller manager |

## Quick start

### Build

```bash
make build          # binaries in ./bin/
make docker-build   # Docker images
```

### Deploy on Kubernetes

```bash
kubectl apply -f deploy/crds/crds.yaml   # install CRDs
kubectl apply -f deploy/daemon.yaml       # daemon DaemonSet
kubectl apply -f deploy/operator.yaml     # operator Deployment
kubectl apply -f deploy/examples.yaml     # example policies
```

### meshctl commands

```bash
meshctl status           # daemon status
meshctl healthz          # health check

# Live packet trace (requires root + pwru in PATH)
meshctl observe -- --filter-dst-ip 10.244.0.5
meshctl observe -- --filter-src-ip 10.244.0.3 --output-tuple
```

## CRDs

### MeshPolicy – mTLS enforcement

```yaml
apiVersion: mesh.minimesh.io/v1alpha1
kind: MeshPolicy
metadata:
  name: strict-mtls
  namespace: production
spec:
  mtlsMode: STRICT   # STRICT | PERMISSIVE | DISABLE
  selector:
    app: payments
```

### MeshCertificate – managed certificates

```yaml
apiVersion: mesh.minimesh.io/v1alpha1
kind: MeshCertificate
metadata:
  name: payments-cert
  namespace: production
spec:
  serviceName: payments
  namespace: production
  ttl: "24h"
```

The operator stores the issued certificate in a `kubernetes.io/tls` Secret
named `minimesh-cert-<serviceName>` and re-issues it every 12 h.

## Observability

`meshctl observe` wraps [Cilium pwru](https://github.com/cilium/pwru)
(eBPF packet tracing) and streams its output to your terminal.
`pwru` must be installed separately and requires root + Linux ≥ 5.5 with BTF.

## mTLS details

- The daemon generates a self-signed CA in memory on startup.
- TLS 1.3 with mutual authentication is enforced on all relayed connections.
- The operator issues per-service certificates via `MeshCertificate` CRs and
  rotates them before expiry (every 12 h by default).