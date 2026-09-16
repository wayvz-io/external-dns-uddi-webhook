# Design

The provider is a sidecar because ExternalDNS defines its webhook API over
localhost. The sidecar converts that API to Infoblox Universal DDI operations.
It does not watch Kubernetes resources itself.

## Request flow

ExternalDNS starts by requesting the provider's domain filter from `GET /`.
During each synchronization, it requests the current records from
`GET /records` and sends desired endpoints to `POST /adjustendpoints`.
ExternalDNS builds the change plan, then sends it to `POST /records`.

The upstream `provider/webhook/api` package serves these routes. Keeping the
server implementation upstream ties the wire protocol to the pinned
ExternalDNS version and avoids a second implementation in this repository.

## Zone and record mapping

The provider resolves `UDDI_VIEW` to a resource ID at startup. It lists the
view's `cloud` primary authoritative zones and keeps only zones allowed by both
the domain filter and `UDDI_ZONE_FILTER`. The provider caches this list for
`UDDI_ZONE_CACHE_TTL`.

For each endpoint, the longest matching zone suffix selects the UDDI zone. One
ExternalDNS endpoint can contain several targets, while one UDDI object stores
one target. The provider therefore creates one UDDI record per target and
groups those records back into an endpoint when reading.

The record ID index uses the name, type, and target. Update plans compare the
old and new target sets, then order deletes before updates and creates. This
order lets a CNAME replace an address record, or the reverse, without a
temporary type conflict.

## TXT ownership and TTLs

The Portal removes the outer quotes from a single TXT character-string.
ExternalDNS's TXT registry uses a quoted value. The provider normalizes both
forms so the registry record does not appear to change during every
synchronization.

The TXT registry remains the ownership record. The `external-dns=true` UDDI
tag makes managed records easier to find in the Portal, but it does not grant
ownership.

UDDI serves a record TTL only when the record overrides inheritance. The
provider sends the override action for an explicit endpoint TTL or
`UDDI_DEFAULT_TTL`. Otherwise, it sends the inherit action and the record uses
the zone default.

## Process boundaries

The webhook API listens on `localhost:8888` because only the ExternalDNS
container needs it. A second listener exposes `/healthz`, `/readyz`, and
`/metrics` on port 8080. Kubernetes probes and Prometheus use that listener.

The image is a static Go binary on a distroless base. It runs as `nonroot` with
a read-only root filesystem and no Linux capabilities.
