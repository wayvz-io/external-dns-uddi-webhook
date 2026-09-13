#!/usr/bin/env bash
# Merge the per-test Go coverprofiles that `bazel coverage //...` writes
# (cover_format=go_cover, see .bazelrc) into ONE file for SonarQube:
#
#   target/sonar/go-coverage.out   (sonar.go.coverage.reportPaths)
#
# Go coverprofiles are line-oriented: a single `mode:` header followed by
# `file:startline.col,endline.col numstmts count` records, so concatenation with
# one header is a valid merged profile (duplicates for the same block across
# tests are summed by consumers in `count` mode and OR'd in `set` mode).
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
  echo "error: no non-empty coverage.dat under $testlogs -- did 'bazel coverage //...' run?" >&2
  exit 1
fi

module="$(awk '/^module / {print $2; exit}' go.mod)"
if [[ -z "$module" ]]; then
  echo "error: could not read the module path from go.mod" >&2
  exit 1
fi

mode="$(grep -h -m1 '^mode:' "${files[@]}" | head -n1)"
if [[ -z "$mode" ]]; then
  echo "error: coverage.dat files carry no 'mode:' header -- is cover_format=go_cover set (see .bazelrc)?" >&2
  exit 1
fi

{
  echo "$mode"
  # Keep only this module's files: rules_go's go_cover profiles also carry blocks
  # for instrumented external packages, which Sonar cannot map and which bloat
  # the report ~100x.
  for f in "${files[@]}"; do
    grep "^${module}/" "$f" || true
  done
} > "$out"

echo "merged ${#files[@]} coverage.dat files -> $out ($(wc -l < "$out") lines)"
