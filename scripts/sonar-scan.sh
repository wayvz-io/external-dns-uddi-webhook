#!/usr/bin/env bash
# Run SonarQube analysis via the Bazel-provisioned sonar-scanner
# (//tools/sonar:scan). This script is the credential front-end only.
# Same entrypoint for CI and local:
#   - CI:    SONAR_HOST_URL + SONAR_TOKEN come from the environment (GH Actions
#            maps SONAR_TOKEN <- secrets.SONAR_HOST_TOKEN).
#   - Local: falls back to 1Password (op://k8s-dev/sonarqube/{website,user-token}).
# Hard-errors loudly if neither resolves -- no silent fallback.
#
# Expects target/sonar/go-coverage.out (scripts/merge-go-coverage.sh) to exist;
# the scan still runs without it, Sonar just reports no coverage.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

host="${SONAR_HOST_URL:-}"
token="${SONAR_TOKEN:-}"

if [[ -z "$host" || -z "$token" ]]; then
  if command -v op >/dev/null 2>&1; then
    : "${host:=$(op read 'op://k8s-dev/sonarqube/website')}"
    : "${token:=$(op read 'op://k8s-dev/sonarqube/user-token')}"
  fi
fi

if [[ -z "$host" || -z "$token" ]]; then
  echo "error: SonarQube credentials unresolved." >&2
  echo "  CI:    set SONAR_HOST_URL and SONAR_TOKEN in the environment." >&2
  echo "  Local: install + sign in the 1Password CLI ('op') so it can read" >&2
  echo "         op://k8s-dev/sonarqube/website and op://k8s-dev/sonarqube/user-token." >&2
  exit 1
fi

if ! command -v bazel >/dev/null 2>&1; then
  echo "error: bazel not on PATH -- run this inside 'nix develop'." >&2
  exit 1
fi

if [[ ! -s target/sonar/go-coverage.out ]]; then
  echo "warning: target/sonar/go-coverage.out missing -- run 'bazel coverage //...' then scripts/merge-go-coverage.sh first." >&2
fi

args=()
[[ -n "${SONAR_PROJECT_KEY:-}" ]] && args+=(-Dsonar.projectKey="$SONAR_PROJECT_KEY")

# The scanner is a local `bazel run` (no remote execution needed): --config=local.
SONAR_HOST_URL="$host" SONAR_TOKEN="$token" \
  exec bazel run --config=local //tools/sonar:scan -- "${args[@]}"
