# external-dns-uddi-webhook

[ExternalDNS](https://github.com/kubernetes-sigs/external-dns) webhook provider
for **Infoblox Universal DDI** (the Infoblox Portal / NIOS-X). Runs as a sidecar
next to `external-dns` and turns its record changes into Portal `dns/record`
calls. Static Go binary on distroless, multi-arch.

Image: `ghcr.io/wayvz-io/external-dns-uddi-webhook` (tags `vX.Y.Z`, `vX.Y`, `latest`; linux/amd64 + linux/arm64)

## Configuration (environment)

| Variable | Default | Purpose |
|---|---|---|
| `INFOBLOX_PORTAL_KEY` | required | Portal API key (DNS data read/write on the view) |
| `INFOBLOX_PORTAL_URL` | `https://csp.infoblox.com` | Portal base URL |
| `UDDI_VIEW` | required | DNS view **name**; resolved to its id at startup |
| `UDDI_ZONE_FILTER` | | Optional comma list restricting which auth zones are managed |
| `DOMAIN_FILTER` | | Comma list of domains; also returned to external-dns on `GET /` |
| `EXCLUDE_DOMAIN_FILTER` | | Domains to exclude |
| `REGEXP_DOMAIN_FILTER` / `REGEXP_DOMAIN_FILTER_EXCLUSION` | | Regex variants |
| `SERVER_HOST` / `SERVER_PORT` | `localhost` / `8888` | Webhook API (keep on localhost) |
| `HEALTHZ_HOST` / `HEALTHZ_PORT` | `0.0.0.0` / `8080` | `/healthz` and `/metrics` |
| `DRY_RUN` | `false` | Log changes instead of applying them |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

Record types: A, AAAA, CNAME, TXT, SRV, MX, NS. Records are tagged
`external-dns=true` in the Portal; ownership is the ExternalDNS TXT registry.

## Deploy with the upstream Helm chart

```yaml
provider:
  name: webhook
  webhook:
    image:
      repository: ghcr.io/wayvz-io/external-dns-uddi-webhook
      tag: v0.1.0
    env:
      - name: INFOBLOX_PORTAL_KEY
        valueFrom:
          secretKeyRef:
            name: uddi-api-key
            key: api_key
      - name: UDDI_VIEW
        value: wayvz-internal
      - name: DOMAIN_FILTER
        value: k3s.lab.wayvz.io
    securityContext:
      runAsNonRoot: true
      readOnlyRootFilesystem: true
      capabilities: { drop: ["ALL"] }
sources: [service, ingress, gateway-httproute]
registry: txt
txtOwnerId: k3s-office
txtPrefix: "reg-%{record_type}."
domainFilters: [k3s.lab.wayvz.io]
policy: sync
interval: 1m
```

The chart already probes `/healthz` on port 8080 of the `webhook` container.
`deploy/kustomize/` has the equivalent raw manifests as a reviewable example.

## Development

```sh
nix develop                          # bazel 9, buildifier, gazelle, go, gopls, crane, kubectl
bazel test //...                     # RBE on the lab Buildbarn (Tailscale)
bazel test --config=local //...      # offline
bazel run //:gazelle                 # regenerate Go BUILD files
bazel mod tidy                       # after adding a direct Go dependency
```

### Bazel targets

| Target | What |
|---|---|
| `//cmd/webhook` | the binary (gazelle-generated) |
| `//:image` | single-arch `oci_image` (distroless static, `nonroot`) |
| `//:image_index` | linux/amd64 + linux/arm64 index |
| `//:load` | `bazel run` -> local podman/docker image `...:dev` |
| `//:push` | push `//:image_index` to ghcr with tags from `//:image_tags` (use `--stamp --workspace_status_command=tools/workspace-status.sh`) |
| `//:image_tags` | stamped tag list (`latest`, `vX.Y.Z`, `vX.Y`) |
| `//:gazelle`, `//:buildifier_check`, `//:buildifier_test` | codegen / lint |
| `//tools/sonar:scan` | hermetic sonar-scanner (`scripts/sonar-scan.sh` wraps it) |

CI runs `bazel coverage` on the in-cluster RBE runner and feeds Sonar on `main`;
releases come from release-please (conventional commits) -> `v*` tag -> image
push + GitHub release. Details: `docs/PLAN.md`.
