# Development

This reference lists the build targets and maintenance commands. Start with the
[contribution guide](../CONTRIBUTING.md) when preparing a change. See the
[design document](DESIGN.md) for the record model and request flow.

## Build commands

The default Bazel configuration runs locally. Enter the Nix development shell,
then run the test suite:

```sh
nix develop
bazel test //...
```

If you use direnv, run `direnv allow` instead of `nix develop`.

Use these commands for repository maintenance:

```sh
bazel run //:gazelle
bazel mod tidy
bazel run @rules_go//go -- mod tidy
bazel run //:buildifier_check
bazel run //:load
bazel coverage //...
scripts/merge-go-coverage.sh
scripts/sonar-scan.sh
```

Run Gazelle after changing Go packages. Run both module tidy commands after
changing `go.mod`. The `//:load` target loads the `:dev` image into Podman or
Docker. The coverage script writes `target/sonar/go-coverage.out` for SonarQube.

## Bazel targets

| Target | Purpose |
|---|---|
| `//cmd/webhook` | Webhook binary. Gazelle generates its build rule. |
| `//:image` | Single-architecture, distroless `oci_image` that runs as `nonroot` |
| `//:image_index` | Image index for `linux/amd64` and `linux/arm64` |
| `//:load` | Local Podman or Docker image tagged `:dev` |
| `//:push` | Pushes `//:image_index` with tags from `//:image_tags` |
| `//:image_tags` | Stamped `latest`, `vX.Y.Z`, and `vX.Y` tag list |
| `//:gazelle` | Regenerates Go build rules |
| `//:buildifier_check` | Checks build-file formatting and lint rules |
| `//:buildifier_test` | Runs the Buildifier check as a test |
| `//tools/sonar:scan` | Runs the pinned SonarScanner CLI |

`//:buildifier_test` belongs to `//...`, so invalid build files fail the test
suite.

## CI and releases

CI runs the same Bazel test graph with remote execution and uploads a merged Go
coverage report. CodeQL checks Go and GitHub Actions. SonarQube scans pushes to
`main`.

Release Please maintains the release pull request. Merging that pull request
creates a `vX.Y.Z` tag and GitHub release. The tag workflow tests the source,
publishes the multi-architecture image to GHCR, and adds generated release
notes.

`//:image_tags` reads `STABLE_GIT_TAG` when stamping is enabled. Without
`--stamp`, the rule emits only `latest`, so an ad hoc push cannot overwrite a
version tag.

Renovate updates Go, Bazel, GitHub Actions, and Nix dependencies. ExternalDNS
updates never merge automatically or cross a major version. Universal DDI SDK
updates get a separate pull request because generated field names can change
between minor releases.
