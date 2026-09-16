# Design of external-dns-uddi-webhook

This document records the design and rollout decisions. See
[`research-notes.md`](research-notes.md) for source material and the
[README](../README.md) for deployment instructions.

## Goals

1. Publish Service, Ingress, and HTTPRoute hostnames from a Kubernetes cluster
   to Infoblox Universal DDI instead of a static DNS template.
2. Use one static Go binary, environment-variable configuration, and a mocked
   SDK in tests. The provider does not use TSIG or zone transfers.
3. Follow the build, CI, and release conventions used by other Wayvz
   repositories. See the [development guide](DEVELOPMENT.md).

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

The sidecar implements `provider.Provider` from `sigs.k8s.io/external-dns`.
The upstream `provider/webhook/api` package serves the interface, so the pinned
ExternalDNS version defines the HTTP contract. The provider converts each
target to one UDDI `dns/record` through `universal-ddi-go-client`. It selects
the `cloud` primary authoritative zone with the longest matching suffix in the
configured view.

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

## Security model

- Writes go to the Portal API over HTTPS with an API key scoped to DNS data
  read/write on the configured view; the key lives in Vault and reaches the
  pod through a `VaultStaticSecret` (path/role decided by the GitOps repo
  managing the deployment). NIOS-X never sees credentials; it only receives
  config from the Portal.
- Use a dedicated `cloud` primary authoritative zone in its own view.
  `--domain-filter` and the provider's `DOMAIN_FILTER`, returned by `GET /`,
  prevent ExternalDNS from reading or writing names outside that zone.
- The TXT registry tracks ownership through `--txt-owner-id` and
  `--txt-prefix`. ExternalDNS does not change records without its matching TXT
  record. The provider also adds the `external-dns=true` Portal tag.
- CNAMEs that point friendly names in a parent zone at the managed sub-zone
  should stay under separate infra-as-code management; ExternalDNS never
  writes to the parent zone.
- The distroless container runs as `nonroot` with UID 65532. Its root
  filesystem is read-only and it has no Linux capabilities. The webhook API
  binds to localhost on port 8888. Only the health and metrics listener on port
  8080 is exposed.

## Suggested rollout

0. Point a development instance at a scratch zone or test view with
   `DRY_RUN=false` and a source that only yields `probe_delete_me*` names.
   Confirm create/update/delete round-trips, TXT quoting stability (no
   perpetual diffs), and that the Portal shows the `external-dns=true` tag.
   Delete the zone afterwards.
1. Deploy against the target zone while the previous DNS
   mechanism keeps serving it. Compare the record set against what it answers.
2. Delegate or forward the zone to the NIOS-X hosts on the
   relevant resolvers. Watch the provider's `/metrics` (API errors, plan
   sizes) and external-dns logs for a few days.
3. Remove the previous DNS mechanism for the sub-zone. Keep any
   parent-zone CNAMEs under their existing management.

## Open questions

- Does the Portal API key model allow scoping to a single view, or only to
  "DNS data" globally? If global, the `DOMAIN_FILTER` is the only guardrail --
  document that in the deployer's runbook.
- TTL semantics (settled 2026-09-14 against a live Portal): a record `ttl` is
  only served when `inheritance_sources.ttl.action` is `override`; the client
  sends `override` with an explicit TTL and `inherit` without one. ExternalDNS
  TTL 0 ("unset") maps to inherit. No perpetual diffs were observed.
- TXT (settled 2026-09-14): the Portal stores a single quoted string without
  its quotes. Record keys compare TXT unquoted and `Records` re-quotes, so the
  registry's quoted form matches on delete/update and the plan stays stable.
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
