# weft-router

Per-tenant-ASN BGP speaker + FIB programmer for openweft. Runs as a
dedicated micro-VM in the data plane, one per tenant that needs BGP
egress (its own public ASN).

**Status: scaffolding.** The lifecycle wiring (cobra root, agent
subcommand, BGP server + FIB programmer + NATS subscriber + metrics)
is in place ; the actual GoBGP integration, netlink push, and NATS
subscriber loop are stubbed with `// TODO:` markers so the daemon
builds and tests green while we wire the rest.

## What it does

- Peers with the upstream ISP over **BGP-4 + EVPN + flowspec** using
  [GoBGP](https://osrg.github.io/gobgp/) (Apache 2.0, pure-Go ; the
  same engine [Calico](https://www.tigera.io/project-calico/) and
  [Cilium](https://cilium.io/) ship in production).
- Filters received routes per tenant policy.
- Installs them in the Linux kernel FIB via [netlink](https://pkg.go.dev/github.com/vishvananda/netlink),
  in a dedicated routing table so tenant routes never collide with
  weft's own management table.
- Advertises the tenant's owned prefixes back out to the ISP.

## How it fits in

```
+-----------------+         +------------------+         +-----------+
|   weft-network  |  NATS   |   weft-router    |  BGP    |  upstream |
|  (control plane)| ──────► | (this — one per  | ───────►|    ISP    |
|                 |         |  tenant ASN)     |         |           |
+-----------------+         +---------┬--------+         +-----------+
                                      │ netlink
                                      ▼
                              +-----------------+
                              |  Linux kernel   |
                              |   FIB (tenant   |
                              |  routing table) |
                              +-----------------+
```

- **Control plane** : [`weft-network`](https://github.com/openweft/weft-network)
  watches `Router` resources in etcd and publishes the per-tenant
  desired state (peers, advertised prefixes, policies) on the NATS
  subject `weft.router.<tenant-uuid>.config` (retained — newly-booted
  routers replay the latest config without weft-network having to chase
  them).
- **Data plane** : this binary, running as a `weft microvm` micro-VM.
  Subscribes to its tenant subject ; every reconcile is idempotent.

## Why Go-native, not VyOS / FRR

See the openweft project doc's networking section, but TL;DR :
GoBGP covers ~all the BGP-4 footprint a cloud tenant ever asks for
(BGP-4 + EVPN + flowspec — Calico/Cilium prove the surface in
production), it's a ~tens-of-MB binary that boots in hundreds of
milliseconds in a micro-VM, and it keeps the platform's
"mono-binary Go, no C in the control plane" thesis honest.

Tenants who genuinely need multi-protocol routing (OSPF / IS-IS /
RSVP-TE) or want to bring their own VyOS / OPNsense config still
run those as classic VMs via `weft instance` — the escape hatch
documented in the openweft handbook.

## Layout

```
cmd/weft-router/        cobra root + `agent` subcommand
internal/config/        static bootstrap config loader + validation
internal/bgp/           GoBGP server wrapper (Start, Stop, Apply…, Routes)
internal/fib/           netlink-based RTM_NEWROUTE/DELROUTE programmer
internal/subscriber/    NATS subscriber that pushes desired-state → bgp
internal/metrics/       Prometheus /metrics on a dedicated port
```

## Build

```sh
# Host build (dev)
pkgx task build

# Cross-build the binaries the micro-VM runs (linux/arm64 + linux/amd64)
pkgx task build-linux
```

## OCI image

```sh
# Multi-arch local build (linux/amd64 + linux/arm64)
docker buildx build --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=$(git describe --tags --always --dirty) \
  --build-arg COMMIT=$(git rev-parse --short HEAD) \
  --build-arg DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  -t ghcr.io/openweft/weft-router:dev .

# Tag-driven publish to ghcr.io/openweft/weft-router (via the
# release.yml workflow's `oci` job — fires on a vX.Y.Z tag or a
# manual workflow_dispatch run).
git tag v0.1.0 && git push --tags
```

Consumed by weft-network's lifecycle path : when a Router with
backend=gobgp is created, the orchestrator spawns
`ghcr.io/openweft/weft-router:<tag>` as a micro-VM with the
tenant's config mounted at `/etc/weft-router/config.hcl` (virtio-fs
/ 9p).

## Run

```sh
./weft-router agent \
    --config /etc/weft-router/config.hcl \
    --metrics-addr :9100
```

Static bootstrap config (`config.hcl`, served into the micro-VM via
virtio-fs/9p by `weft-network`) needs : `tenant_uuid`, `local_asn`,
`router_id`, `nats_url`.

## License

BSD 3-Clause — see [LICENSE](LICENSE). Same license as the rest of openweft.
