# Build and wire-up plan: external-dns-uddi-webhook

Status: scaffold. Companion to `research-notes.md` (verified facts, sources) and
`../README.md` (usage). This file is the "why and in what order".

## Goals

1. Let ExternalDNS in the `k3s-office` cluster publish Service / Ingress /
   HTTPRoute hostnames into Infoblox Universal DDI (the Portal, served by the
   NIOS-X host `niosx1`) instead of the CoreDNS wildcard template.
2. Keep the provider small: one static Go binary, env-only config, mocked SDK
   in tests, no TSIG/zone-transfer plumbing.
3. Same build/CI/release shape as the other wayvz repos (Bazel bzlmod on the
   lab Buildbarn, Sonar on main, release-please, Renovate, SHA-pinned actions)
   so there is nothing new to operate.

## Architecture

```
 k3s-office cluster                                  Infoblox SaaS
+----------------------------------------------+    +------------------------+
| external-dns pod                             |    |                        |
|  +----------------+   localhost:8888         |    |  Universal DDI Portal  |
|  | external-dns   |------------------------->|--->|  csp.infoblox.com      |
|  | (upstream,     |  GET /  (DomainFilter)   |    |  /api/ddi/v1/dns/*     |
|  |  --provider=   |  GET /records            |    |  (API key, view        |
|  |   webhook)     |  POST /records (changes) |    |   wayvz-internal)      |
|  |                |  POST /adjustendpoints   |    +-----------+------------+
|  +-------+--------+                          |                | config push
|          | watches Service/Ingress/HTTPRoute  |                v
|  +-------+--------+   :8080 /healthz /metrics|    +------------------------+
|  | webhook sidecar|<---- probes / Prometheus  |    |  niosx1 (NIOS-X host)  |
|  | (this repo)    |                          |    |  authoritative DNS for |
|  +----------------+                          |    |  k3s.lab.wayvz.io      |
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
.github/workflows/                        ci, release-please, release, codeql
docs/                                     this plan + research notes
```

## Build and test locally

```sh
nix develop                        # or: direnv allow
bazel test //...                   # RBE on bb.lab.wayvz.io (needs Tailscale)
bazel test --config=local //...    # offline, everything local
bazel run //:gazelle               # regenerate cmd/** internal/** BUILD files
bazel mod tidy                     # refresh use_repo(go_deps, ...) after go.mod changes
bazel run @rules_go//go -- mod tidy
bazel run //:buildifier_check
bazel run --config=local //:load   # image -> local podman/docker as :dev
bazel coverage //... && scripts/merge-go-coverage.sh   # -> target/sonar/go-coverage.out
scripts/sonar-scan.sh              # local Sonar scan (creds via env or `op`)
```

`.bazelrc` defaults to the lab Buildbarn exactly like the bombora repo (plain
`build` lines, `--jobs=4`, BES to bb-portal). `--config=local` empties the
endpoints; `--config=remote` is the CI flavour (build-without-the-bytes, the
workflow re-points executor/cache at the bb-clientd unix socket). Per-machine
overrides go in `.bazelrc.user` (see `.bazelrc.user.example`).

## CI

`ci.yml`, job `Test (RBE)` on the in-cluster `bb-ci-worker` runner
(Bazel 9.1.1 + bb-clientd preinstalled, warm output base on `/clientd`):

1. `bazel coverage --config=remote //...` through the clientd socket, with the
   retry-once wrapper. One instrumented pass = build + every test + a Go
   coverprofile per test (`cover_format=go_cover`; Sonar's Go analyzer reads
   native coverprofiles, not lcov, so Bazel's lcov merger is disabled).
2. `scripts/merge-go-coverage.sh` concatenates `bazel-testlogs/**/coverage.dat`
   under one `mode:` header into `target/sonar/go-coverage.out`; the job fails if
   that is empty (an empty report would silently zero Sonar coverage).
3. Upload artifact `go-coverage`.

`sonar-scan` (push to main / dispatch only, `needs: rbe`, `runner-nix-amd64`)
downloads the artifact and runs `nix develop --command bazel run --config=local
//tools/sonar:scan`; the scanner is a JRE-bundled `http_archive`, so no Java in
the devshell. `codeql.yml` runs Go (manual `go build ./...`) and Actions
analysis. `//:buildifier_test` is part of `//...`, so BUILD formatting gates CI.

## Release flow

```
conventional commits on main
   -> release-please.yml maintains "chore(main): release x.y.z" PR (CHANGELOG, version)
   -> merge PR => tag vX.Y.Z + GitHub release stub
   -> release.yml (on tag, bb-ci-worker):
        bazel test --config=remote //...
        write $DOCKER_CONFIG/config.json from GITHUB_TOKEN (no docker CLI needed)
        bazel run --stamp --workspace_status_command=tools/workspace-status.sh //:push
          -> ghcr.io/wayvz-io/external-dns-uddi-webhook:{vX.Y.Z,vX.Y,latest} (amd64+arm64 index)
        softprops/action-gh-release (auto-generated notes)
```

`//:image_tags` is a stamped genrule reading `STABLE_GIT_TAG`; without `--stamp`
it emits only `latest`, so an ad-hoc `bazel run //:push` cannot overwrite a
semver tag by accident.

## Renovate policy

`renovate.json`: `config:recommended` + pinned action digests + `group:allNonMajor`
+ `:automergeMinor`, dependency dashboard, OSV alerts, `minimumReleaseAge: 3 days`
(waived for vulnerability alerts). Managers: `gomod` (with `gomodTidy`,
`gomodUpdateImportPaths`, indirect deps enabled because `sigs.k8s.io/external-dns`
drags in the k8s/cloud SDK tree where CVEs land), `bazel-module` (bazel_dep +
distroless `oci.pull` digest), `github-actions`, `nix`. Exceptions:
`sigs.k8s.io/external-dns` never auto-merges and never takes a major;
`universal-ddi-go-client` gets its own PR. The `bazel-module` lockfile refresh
needs `bazelModDeps` in the Renovate CE server's `allowedUnsafeExecutions`.

## Security model (no zone transfers, no TSIG)

- Writes go to the Portal API over HTTPS with an API key scoped to DNS data
  read/write on the `wayvz-internal` view; the key lives in Vault and reaches
  the pod through a `VaultStaticSecret` (path/role decided in iac). NIOS-X
  never sees credentials; it only receives config from the Portal.
- The sub-zone `k3s.lab.wayvz.io` is a dedicated `cloud`-primary auth zone
  under the `wayvz-internal` view. `--domain-filter=k3s.lab.wayvz.io` plus the
  provider's own `DOMAIN_FILTER` (returned on `GET /`) mean ExternalDNS can
  neither read nor write anything outside it, even with a broader key.
- Ownership is the TXT registry (`--txt-owner-id=k3s-office`,
  `--txt-prefix=reg-%{record_type}.`); records without a matching owner TXT are
  never touched, so hand-made or Terraform-made records in the same zone are
  safe. Records are additionally tagged `external-dns=true` for the Portal UI.
- CNAMEs that point friendly names in the parent `lab.wayvz.io` zone at
  `*.k3s.lab.wayvz.io` stay under Terraform in the iac `dns-lab` workspace;
  ExternalDNS never writes to the parent zone.
- Container: distroless static, `nonroot` (65532), read-only rootfs, all caps
  dropped; 8888 bound to localhost, only 8080 (health/metrics) exposed.

## Rollout phases

0. **Probe.** Point a dev copy at a scratch zone (`scratch.lab.wayvz.io` or a
   test view) with `DRY_RUN=false` and a source that only yields
   `probe_delete_me*` names. Confirm create/update/delete round-trips, TXT
   quoting stability (no perpetual diffs), and that the Portal shows the
   `external-dns=true` tag. Delete the zone afterwards.
1. **Mirror.** Deploy for real against `k3s.lab.wayvz.io` while CoreDNS keeps
   serving the wildcard. Nothing resolves via NIOS-X yet; compare the record set
   against what the wildcard answers (`dig @niosx1`).
2. **Split-DNS cutover.** Delegate/forward `k3s.lab.wayvz.io` to `niosx1` on the
   lab resolvers (and the Tailscale split-DNS config). Watch the provider's
   `/metrics` (API errors, plan sizes) and external-dns logs for a few days.
3. **Retire.** Remove the CoreDNS wildcard template for the sub-zone; keep the
   parent-zone CNAMEs in Terraform.

## One-time manual steps

- [ ] SonarQube: create project `external-dns-uddi-webhook` (key must match
      `sonar-project.properties`), generate a project analysis token.
- [ ] GitHub repo secrets: `SONAR_HOST_URL` (`https://sonarqube.lab.wayvz.io`)
      and `SONAR_HOST_TOKEN`.
- [x] Renovate CE: repo added to `MEND_RNV_AUTODISCOVER_FILTER` (iac PR #540).
      Still to check: `bazelModDeps` in `allowedUnsafeExecutions` and bazelisk
      in the Renovate image, or `MODULE.bazel.lock` refreshes will be skipped.
- [x] GitHub App installs: Renovate, SonarQube and ARC apps are installed on
      all org repos; nothing to grant.
- [x] Rulesets on `main`: signed commits + wayvz.io author/committer emails
      (all branches), and deletion/force-push protection + required status
      check `Test (RBE)` on the default branch.
- [x] Org secrets `AUTOUPDATE_APP_ID` / `AUTOUPDATE_APP_PRIVATE_KEY` granted to
      the repo (release-please pushes tags with that App token).
- [ ] Infoblox Portal: mint a least-privilege API key (DNS data read/write on
      view `wayvz-internal` only), store it in Vault; wire the
      `VaultStaticSecret` path/role in iac.
- [ ] Infoblox Portal: create auth zone `k3s.lab.wayvz.io` (primary type
      `cloud`) in view `wayvz-internal`, served by `niosx1`.
- [ ] iac `dns-lab` workspace: parent-zone CNAMEs -> `*.k3s.lab.wayvz.io`.
- [x] First push: gazelle, `bazel mod tidy`, `bazel test //...` (RBE and
      local) green; `MODULE.bazel.lock` committed; release-please opened PR #1.
- [ ] Packages: make the ghcr package visible to the cluster's pull secret
      (private by default for a private repo).

## Open questions

- Does the Portal API key model allow scoping to a single view, or only to
  "DNS data" globally? If global, the `DOMAIN_FILTER` is the only guardrail --
  document that in the runbook.
- TTL semantics: UDDI `ttl` is optional (inherits zone default); ExternalDNS
  sends TTL 0 for "unset". Confirm `AdjustEndpoints` mapping does not create
  perpetual TTL diffs.
- `oci_image_index` via `platform_transition_filegroup` vs the rules_oci
  `platforms=` attribute: the former is what the other repos use; switch if
  rules_oci marks the latter stable.
- Should `release.yml` also sign the image (cosign keyless) and attach SBOM /
  provenance? Deferred until the cluster verifies signatures.
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
