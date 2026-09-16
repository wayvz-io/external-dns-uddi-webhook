# Contributing

## Prepare a change

1. Enter the development shell with `nix develop`, or allow the direnv
   configuration with `direnv allow`.
2. Make the change and add tests for behavior that can regress.
3. Run `bazel test //...`.
4. If you changed Go package structure, run `bazel run //:gazelle`.
5. If you changed `go.mod`, run `bazel run @rules_go//go -- mod tidy` and
   `bazel mod tidy`.

The default Bazel configuration runs locally. Wayvz CI selects its remote
execution configuration explicitly.

## Submit a pull request

Use a conventional commit such as `fix(provider): preserve inherited TTL` or
`docs: clarify webhook configuration`. Keep generated files with the change
that produces them.

Describe the user-visible effect and the checks you ran. Keep unrelated
formatting or dependency updates out of the pull request.

See the [development reference](docs/DEVELOPMENT.md) for the available targets
and the [design document](docs/DESIGN.md) for implementation constraints.
