{
  description = "ExternalDNS webhook provider for Infoblox Universal DDI - dev shell";

  inputs = {
    # Bazel 9 is only in unstable (matches .bazelversion / the bb-ci-worker runner).
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    { nixpkgs, flake-utils, ... }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs { inherit system; };
      in
      {
        devShells.default = pkgs.mkShell {
          name = "external-dns-uddi-webhook-dev";

          packages = with pkgs; [
            # Build
            bazel_9
            bazel-buildtools # buildifier
            bazel-gazelle

            # Go (Bazel provides the build toolchain; these are for editor tooling)
            go
            gopls
            golangci-lint

            # Ops conveniences
            jq
            crane # inspect/copy the pushed image
            kubectl
          ];

          shellHook = ''
            # NixOS has no /bin/bash, which .bazelrc pins for remote actions.
            # Point --config=local at the host bash via the gitignored .bazelrc.user.
            line="common:local --shell_executable=$(command -v bash)"
            grep -qsF "$line" .bazelrc.user || echo "$line" >> .bazelrc.user
            echo "external-dns-uddi-webhook: bazel $(bazel --version 2>/dev/null | cut -d' ' -f2), $(go version | cut -d' ' -f3)"
            echo "  bazel test //...               (RBE on the lab Buildbarn over Tailscale)"
            echo "  bazel test --config=local //... (offline)"
          '';
        };
      }
    );
}
