# Build and wire-up plan: external-dns-uddi-webhook

Status: scaffold. Companion to `research-notes.md` (verified facts, sources) and
`../README.md` (usage). This file is the "why and in what order".

## Goals

1. Let ExternalDNS in a Kubernetes cluster publish Service / Ingress /
   HTTPRoute hostnames into Infoblox Universal DDI (the Portal, served by the
   NIOS-X host(s) authoritative for the zone) instead of a static DNS template.
2. Keep the provider small: one static Go binary, env-only config, mocked SDK
   in tests, no TSIG/zone-transfer plumbing.
3. Same build/CI/release shape as the other wayvz repos (Bazel bzlmod on the
   lab Buildbarn, Sonar on main, release-please, Renovate, SHA-pinned actions)
   so there is nothing new to operate. See `DEVELOPMENT.md`.

## Architecture

```
 Kubernetes cluster                                  Infoblox SaaS
+----------------------------------------------+    +------------------------+
| external-dns pod                             |    |                        |
|  +----------------+   localhost:8888         |    |  Universal DDI Portal  |
|  | external-dns   |------------------------->|--->|  csp.infoblox.com      |
|  | (upstream,     |  GET /  (DomainFilter)   |    |  /api/ddi/v1/dns/*     |
|  |  --provider=   |  GET /records            |    |  (API key, view        |
|  |   webhook)     |  POST /records (changes) |    |   my-view)             |
|  |                |  POST /adjustendpoints   |    +-----------+------------+
|  +-------+--------+                          |                | config push
|          | watches Service/Ingress/HTTPRoute  |                v
|  +-------+--------+   :8080 /healthz /metrics|    +------------------------+
|  | webhook sidecar|<---- probes / Prometheus  |    |  NIOS-X host(s)        |
|  | (this repo)    |                          |    |  authoritative DNS for |
|  +----------------+                          |    |  k8s.example.com       |
+----------------------------------------------+    +-----------+------------+
                                                                 | queries
                       clients / resolvers (split-DNS) ----------+
```

The sidecar implements `provider.Provider` from `sigs.k8s.io/external-dns` and
is served by upstream's `provider/webhook/api` package, so the HTTP contract is
whatever the pinned external-dns version expects. It translates endpoints to
UDDI `dns/record` objects via `universal-ddi-go-client`, one UDDI record per
target, zone chosen by longest-suffix match against the `cloud`-primary auth
zones in the configured view.

## Repo layout

```
MODULE.bazel / .bazelrc / .bazelversion   bzlmod root; RBE defaults; 9.1.1
BUILD.bazel                               gazelle, buildifier lint, image targets
platforms/                                linux_amd64 / linux_arm64 for the image index
tools/remote-toolchains/                  RBE exec platform (act-22.04 queue)
tools/sonar/                              hermetic sonar-scanner (bazel run)
tools/workspace-status.sh                 --stamp provider (STABLE_GIT_TAG)
scripts/                                  check_buildifier, merge-go-coverage, sonar-scan
cmd/webhook/                              main
internal/config|uddi|provider|server      Go packages (+ tests, mocked UDDI client)
deploy/kustomize/                         EXAMPLE manifests (not applied from here)
.github/workflows/                        ci, release-please, release
docs/                                     this plan, development guide, research notes
```

## Security model (no zone transfers, no TSIG)

- Writes go to the Portal API over HTTPS with an API key scoped to DNS data
  read/write on the configured view; the key lives in Vault and reaches the
  pod through a `VaultStaticSecret` (path/role decided by the GitOps repo
  managing the deployment). NIOS-X never sees credentials; it only receives
  config from the Portal.
- The managed zone should be a dedicated `cloud`-primary auth zone under its
  own view. `--domain-filter` plus the provider's own `DOMAIN_FILTER`
  (returned on `GET /`) mean ExternalDNS can neither read nor write anything
  outside it, even with a broader key.
- Ownership is the TXT registry (`--txt-owner-id`, `--txt-prefix`); records
  without a matching owner TXT are never touched, so hand-made or
  Terraform-made records in the same zone are safe. Records are additionally
  tagged `external-dns=true` for the Portal UI.
- CNAMEs that point friendly names in a parent zone at the managed sub-zone
  should stay under separate infra-as-code management; ExternalDNS never
  writes to the parent zone.
- Container: distroless static, `nonroot` (65532), read-only rootfs, all caps
  dropped; 8888 bound to localhost, only 8080 (health/metrics) exposed.

## Suggested rollout

0. **Probe.** Point a dev copy at a scratch zone (or a test view) with
   `DRY_RUN=false` and a source that only yields `probe_delete_me*` names.
   Confirm create/update/delete round-trips, TXT quoting stability (no
   perpetual diffs), and that the Portal shows the `external-dns=true` tag.
   Delete the zone afterwards.
1. **Mirror.** Deploy for real against the target zone while the previous DNS
   mechanism keeps serving it. Compare the record set against what it answers.
2. **Cutover.** Delegate/forward the zone to the NIOS-X host(s) on the
   relevant resolvers. Watch the provider's `/metrics` (API errors, plan
   sizes) and external-dns logs for a few days.
3. **Retire.** Remove the previous DNS mechanism for the sub-zone; keep any
   parent-zone CNAMEs under their existing management.

## Open questions

- Does the Portal API key model allow scoping to a single view, or only to
  "DNS data" globally? If global, the `DOMAIN_FILTER` is the only guardrail --
  document that in the deployer's runbook.
- TTL semantics: UDDI `ttl` is optional (inherits zone default); ExternalDNS
  sends TTL 0 for "unset". Confirm `AdjustEndpoints` mapping does not create
  perpetual TTL diffs.
- `oci_image_index` via `platform_transition_filegroup` vs the rules_oci
  `platforms=` attribute: the former is what the other repos use; switch if
  rules_oci marks the latter stable.
- Should `release.yml` also sign the image (cosign keyless) and attach SBOM /
  provenance? Deferred until a consumer verifies signatures.
- `bazel-module` Renovate updates need `MODULE.bazel.lock` committed; decide
  whether to commit it (recommended) on the first PR.
- `--instrumentation_filter=//...` notwithstanding, rules_go go_cover profiles
  under `bazel coverage` carried blocks for external packages (caarlos0/env,
  external-dns, ...). `scripts/merge-go-coverage.sh` filters to the module path;
  check whether a rules_go flag avoids instrumenting deps in the first place.
- cc toolchain: pure Go, yet `//tools/cpp:toolchain_type` must
  resolve for `bazel run @rules_go//go`, rules_pkg's py_binary launcher and the
  coverage lcov_merger. Host gcc autodetection is off (no gcc in the devshell or
  on bb-ci-worker); `hermetic_cc_toolchain` (zig) satisfies resolution and the
  app layer uses aspect `tar` instead of `pkg_tar`. Revisit if rules_go drops
  the requirement.
