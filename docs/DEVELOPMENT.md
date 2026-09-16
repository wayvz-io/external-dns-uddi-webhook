# Development

This guide covers the build, test, and release workflows. See the
[README](../README.md) for deployment configuration and the [design](PLAN.md)
for implementation decisions.

## Build and test locally

Enter the Nix development shell, then run the test suite locally:

```sh
nix develop
bazel test --config=local //...
```

If you use direnv, run `direnv allow` instead of `nix develop`.

Use these commands for common maintenance tasks:

```sh
bazel run //:gazelle
bazel mod tidy
bazel run @rules_go//go -- mod tidy
bazel run //:buildifier_check
bazel run --config=local //:load
bazel coverage //...
scripts/merge-go-coverage.sh
scripts/sonar-scan.sh
```

Run Gazelle after you change Go packages. Run both module tidy commands after
you change `go.mod`. The `//:load` target loads the `:dev` image into Podman or
Docker. The coverage script writes `target/sonar/go-coverage.out` for SonarQube.

### Bazel targets

| Target | Purpose |
|---|---|
| `//cmd/webhook` | Webhook binary. Gazelle generates its build rule. |
| `//:image` | Single-architecture, distroless `oci_image` that runs as `nonroot` |
| `//:image_index` | Image index for `linux/amd64` and `linux/arm64` |
| `//:load` | Local Podman or Docker image tagged `:dev` |
| `//:push` | Pushes `//:image_index` to GHCR with tags from `//:image_tags` |
| `//:image_tags` | Stamped `latest`, `vX.Y.Z`, and `vX.Y` tag list |
| `//:gazelle` | Regenerates Go build rules |
| `//:buildifier_check` | Checks build-file formatting and lint rules |
| `//:buildifier_test` | Runs the Buildifier check as a test |
| `//tools/sonar:scan` | Runs the pinned SonarScanner CLI |

`//:buildifier_test` belongs to `//...`, so an invalid build file fails CI.

## Use the build infrastructure

`.bazelrc` uses the Wayvz Buildbarn cluster by default and limits Bazel to four
jobs. If you cannot access that cluster, pass `--config=local`. The local
configuration clears the remote endpoints and runs without remote execution.

CI uses `--config=remote` and connects through the `bb-clientd` Unix socket.
Put machine-specific settings in `.bazelrc.user`. Copy
`.bazelrc.user.example` as a starting point.

### CI

The `Test (RBE)` job in `.github/workflows/ci.yml` runs on the in-cluster
`bb-ci-worker`. The runner has Bazel 9.1.1, `bb-clientd`, and a persistent
output base at `/clientd`.

1. The job runs `bazel coverage --config=remote //...` through the client
   socket. A wrapper retries the command once. The command builds the project,
   runs every test, and writes one Go coverage profile per test. Sonar reads
   native Go profiles, so the configuration disables Bazel's LCOV merger.
2. `scripts/merge-go-coverage.sh` concatenates `bazel-testlogs/**/coverage.dat`
   under one `mode:` header in `target/sonar/go-coverage.out`. The job rejects
   an empty report because Sonar would otherwise report zero coverage.
3. The job uploads the result as the `go-coverage` artifact.

The `sonar-scan` job runs after pushes to `main` and manual dispatches. It
downloads the coverage artifact and runs
`bazel run --config=local //tools/sonar:scan`. The Bazel target includes a JRE,
so the runner does not need Java or the development shell.

The job exchanges its GitHub OIDC token for a short-lived Vault token under the
`external-dns-uddi-webhook-ci` role. It then reads the shared SonarQube token.
The repository stores neither credential. The CodeQL workflow checks Go and
GitHub Actions on pushes, pull requests, and its weekly schedule.

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

`//:image_tags` reads `STABLE_GIT_TAG` when stamping is enabled. Without
`--stamp`, the rule emits only `latest`. An ad hoc `bazel run //:push` therefore
cannot overwrite a version tag.

### Renovate policy

Renovate pins GitHub Action digests and groups non-major updates. It
automatically merges minor, patch, pin, and digest updates after three days.
Vulnerability alerts do not wait three days.

The configuration manages Go modules, Bazel modules, GitHub Actions, and Nix
dependencies. It also updates indirect Go dependencies because ExternalDNS
depends on the Kubernetes and cloud SDK trees. ExternalDNS updates never merge
automatically or cross a major version. The Universal DDI client gets a
separate pull request. Bazel lockfile updates require `bazelModDeps` in the
Renovate CE server's `allowedUnsafeExecutions` setting.

## Check the Wayvz repository settings

- Create the `external-dns-uddi-webhook` SonarQube project.
- Configure the post-merge scan to use the `external-dns-uddi-webhook-ci`
  Vault role and read `kv/sonarqube/default/user-token`.
- Add the repository to `MEND_RNV_AUTODISCOVER_FILTER`. Allow `bazelModDeps`
  and include Bazelisk in the Renovate image so Renovate can update
  `MODULE.bazel.lock`.
- Install the Renovate, SonarQube, and ARC GitHub Apps for the organization.
- Require signed commits and `wayvz.io` author and committer addresses. Protect
  `main` from deletion and force pushes, and require the `Test (RBE)` check.
- Grant the repository access to `AUTOUPDATE_APP_ID` and
  `AUTOUPDATE_APP_PRIVATE_KEY`. Release Please uses this app to push tags.
- Give the repository access to the `bb-ci-worker` and `runner-nix-amd64`
  runner group.
