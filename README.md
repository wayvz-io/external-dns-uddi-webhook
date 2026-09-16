# external-dns-uddi-webhook

[ExternalDNS](https://github.com/kubernetes-sigs/external-dns) webhook provider
for Infoblox Universal DDI, also known as the Infoblox Portal or NIOS-X. The
webhook runs beside `external-dns` and sends its record changes to the Portal
`dns/record` API.

The container image is
`ghcr.io/wayvz-io/external-dns-uddi-webhook`. Releases publish the `vX.Y.Z`,
`vX.Y`, and `latest` tags for `linux/amd64` and `linux/arm64`.

## Configure the webhook

| Variable | Default | Purpose |
|---|---|---|
| `INFOBLOX_PORTAL_KEY` | required | Portal API key with DNS data read and write access to the view |
| `INFOBLOX_PORTAL_URL` | `https://csp.infoblox.com` | Portal base URL |
| `UDDI_VIEW` | required | DNS view name. The webhook resolves the name to an ID at startup. |
| `UDDI_ZONE_FILTER` | | Optional comma-separated list of zone FQDNs. The webhook lists and writes only these zones. |
| `DOMAIN_FILTER` | | Comma-separated list of domains. The webhook also returns this list from `GET /`. |
| `EXCLUDE_DOMAIN_FILTER` | | Comma-separated list of domains to exclude |
| `REGEXP_DOMAIN_FILTER` | | Regular expression that selects domains |
| `REGEXP_DOMAIN_FILTER_EXCLUSION` | | Regular expression that excludes domains |
| `SERVER_HOST` | `localhost` | Webhook API host. Keep this listener on localhost. |
| `SERVER_PORT` | `8888` | Webhook API port |
| `HEALTHZ_HOST` | `0.0.0.0` | Health and metrics host |
| `HEALTHZ_PORT` | `8080` | Port for `/healthz` and `/metrics` |
| `DRY_RUN` | `false` | Log changes instead of applying them |
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, or `error` |

The provider manages A, AAAA, CNAME, TXT, SRV, MX, and NS records. It adds the
`external-dns=true` Portal tag and uses the ExternalDNS TXT registry to track
ownership. A TTL from an annotation or `UDDI_DEFAULT_TTL` overrides the zone
default. Records without an explicit TTL inherit the zone default.

## Deploy with the upstream Helm chart

```yaml
provider:
  name: webhook
  webhook:
    image:
      repository: ghcr.io/wayvz-io/external-dns-uddi-webhook
      tag: v0.2.0
    env:
      - name: INFOBLOX_PORTAL_KEY
        valueFrom:
          secretKeyRef:
            name: uddi-api-key
            key: api_key
      - name: UDDI_VIEW
        value: my-view
      - name: DOMAIN_FILTER
        value: k8s.example.com
    securityContext:
      runAsNonRoot: true
      readOnlyRootFilesystem: true
      capabilities: { drop: ["ALL"] }
sources: [service, ingress, gateway-httproute]
registry: txt
txtOwnerId: my-cluster
txtPrefix: "reg-%{record_type}."
domainFilters: [k8s.example.com]
policy: sync
interval: 1m
```

The chart probes `/healthz` on port 8080 of the `webhook` container. The
[`deploy/kustomize`](deploy/kustomize) directory contains equivalent Kubernetes
manifests for review or adaptation.

## Development

See the [development guide](docs/DEVELOPMENT.md) to build, test, or release the
project.

## Licence

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
