#!/usr/bin/env bash
# Workspace status command for stamped builds.
#   bazel run --stamp --workspace_status_command=tools/workspace-status.sh //:push
#
# STABLE_GIT_TAG is set only when HEAD is at a tag. Tag workflows use
# GITHUB_REF_NAME; local runs use `git describe --exact-match`.
set -euo pipefail

tag=""
if [[ "${GITHUB_REF_TYPE:-}" == "tag" && -n "${GITHUB_REF_NAME:-}" ]]; then
  tag="$GITHUB_REF_NAME"
else
  tag="$(git describe --tags --exact-match 2>/dev/null || true)"
fi

commit="$(git rev-parse HEAD 2>/dev/null || echo unknown)"

echo "STABLE_GIT_COMMIT ${commit}"
if [[ -n "$tag" ]]; then
  echo "STABLE_GIT_TAG ${tag}"
fi
