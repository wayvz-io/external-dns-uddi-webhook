#!/usr/bin/env bash
# Merge the Go profiles from `bazel coverage //...` for SonarQube.
#
#   target/sonar/go-coverage.out   (sonar.go.coverage.reportPaths)
#
# A Go coverage profile has one `mode:` header followed by coverage records.
# The merged file keeps one header and concatenates the records.
#
# Usage: scripts/merge-go-coverage.sh [testlogs-dir] [out-file]
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

testlogs="${1:-}"
out="${2:-target/sonar/go-coverage.out}"

if [[ -z "$testlogs" ]]; then
  # `bazel info` must see the same startup options as the coverage run; callers
  # with a non-default --output_user_root pass the directory explicitly.
  testlogs="$(bazel info bazel-testlogs 2>/dev/null || echo bazel-testlogs)"
fi

mapfile -t files < <(find "$testlogs" -name coverage.dat -type f -size +0 2>/dev/null | sort)

mkdir -p "$(dirname "$out")"
if [[ ${#files[@]} -eq 0 ]]; then
  echo "error: no non-empty coverage.dat under $testlogs; run 'bazel coverage //...' first" >&2
  exit 1
fi

module="$(awk '/^module / {print $2; exit}' go.mod)"
if [[ -z "$module" ]]; then
  echo "error: could not read the module path from go.mod" >&2
  exit 1
fi

mode="$(grep -h -m1 '^mode:' "${files[@]}" | head -n1)"
if [[ -z "$mode" ]]; then
  echo "error: coverage.dat files have no 'mode:' header; check cover_format in .bazelrc" >&2
  exit 1
fi

{
  echo "$mode"
  # Drop external-package blocks because Sonar cannot map them to this module.
  for f in "${files[@]}"; do
    grep "^${module}/" "$f" || true
  done
} > "$out"

echo "merged ${#files[@]} coverage.dat files -> $out ($(wc -l < "$out") lines)"
