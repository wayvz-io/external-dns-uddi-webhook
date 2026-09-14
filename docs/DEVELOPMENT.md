# Development

Everything a contributor needs to build, test, and release this repo.
User-facing docs live in [`../README.md`](../README.md); design docs live in
[`PLAN.md`](PLAN.md).

## Build and test locally

```sh
nix develop                        # or: direnv allow
bazel test //...                   # RBE (see "Build infrastructure" below)
bazel test --config=local //...    # offline, everything local
bazel run //:gazelle               # regenerate cmd/** internal/** BUILD files
bazel mod tidy                     # refresh use_repo(go_deps, ...) after go.mod changes
bazel run @rules_go//go -- mod tidy
bazel run //:buildifier_check
bazel run --config=local //:load   # image -> local podman/docker as :dev
bazel coverage //... && scripts/merge-go-coverage.sh   # -> target/sonar/go-coverage.out
scripts/sonar-scan.sh              # local Sonar scan (creds via env or `op`)
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

`//:buildifier_test` is part of `//...`, so BUILD formatting gates CI.

## Build infrastructure (wayvz lab infrastructure; external contributors use `--config=local`)

`.bazelrc` defaults to the lab Buildbarn RBE cluster (plain `build` lines,
`--jobs=4`, BES to bb-portal) — this matches the other wayvz repos so there is
nothing new to operate. External contributors without access to that cluster
should use `bazel test --config=local //...` for everything, which empties
the RBE endpoints and runs fully offline. `--config=remote` is the CI flavour
(build-without-the-bytes; the workflow re-points executor/cache at the
bb-clientd unix socket). Per-machine overrides go in `.bazelrc.user` (see
`.bazelrc.user.example`).

### CI

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
the devshell. It authenticates to Vault via GitHub OIDC — no repo secrets are
needed. SonarQube is the static analysis / quality gate for this repo.

### Release flow

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

### Renovate policy

`renovate.json`: `config:recommended` + pinned action digests + `group:allNonMajor`
+ `:automergeMinor`, dependency dashboard, OSV alerts, `minimumReleaseAge: 3 days`
(waived for vulnerability alerts). Managers: `gomod` (with `gomodTidy`,
`gomodUpdateImportPaths`, indirect deps enabled because `sigs.k8s.io/external-dns`
drags in the k8s/cloud SDK tree where CVEs land), `bazel-module` (bazel_dep +
distroless `oci.pull` digest), `github-actions`, `nix`. Exceptions:
`sigs.k8s.io/external-dns` never auto-merges and never takes a major;
`universal-ddi-go-client` gets its own PR. The `bazel-module` lockfile refresh
needs `bazelModDeps` in the Renovate CE server's `allowedUnsafeExecutions`.

## Repo-level setup checklist (wayvz lab infrastructure)

- SonarQube: project `external-dns-uddi-webhook` exists.
- Sonar credentials: no repo secrets. The post-merge scan runs on
  `bb-ci-worker`, logs in to Vault with its GitHub OIDC token (JWT role
  `external-dns-uddi-webhook-ci`, iac `vault-config`) and reads the shared
  `kv/sonarqube/default/user-token` leaf. The host URL is the in-cluster
  Service and is not secret.
- Renovate CE: repo added to `MEND_RNV_AUTODISCOVER_FILTER`. Still to check:
  `bazelModDeps` in `allowedUnsafeExecutions` and bazelisk in the Renovate
  image, or `MODULE.bazel.lock` refreshes will be skipped.
- GitHub App installs: Renovate, SonarQube and ARC apps are installed on all
  org repos; nothing to grant.
- Rulesets on `main`: signed commits + wayvz.io author/committer emails (all
  branches), and deletion/force-push protection + required status check
  `Test (RBE)` on the default branch.
- Org secrets `AUTOUPDATE_APP_ID` / `AUTOUPDATE_APP_PRIVATE_KEY` granted to the
  repo (release-please pushes tags with that App token).
- Runner group: self-hosted ARC runners (`bb-ci-worker`, `runner-nix-amd64`)
  scoped to this repo/org.
