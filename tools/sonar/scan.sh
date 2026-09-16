#!/usr/bin/env bash
# Run the pinned SonarScanner against the workspace.
# The first argument is the scanner launcher path supplied by BUILD.bazel.
# Change to BUILD_WORKSPACE_DIRECTORY because the scanner needs the source tree
# and target/sonar/go-coverage.out instead of the runfiles tree.
# --- begin runfiles.bash initialization v3 ---
# shellcheck disable=SC1090,SC1091
set -uo pipefail
set +e
f=bazel_tools/tools/bash/runfiles/runfiles.bash
source "${RUNFILES_DIR:-/dev/null}/$f" 2>/dev/null ||
  source "$(grep -sm1 "^$f " "${RUNFILES_MANIFEST_FILE:-/dev/null}" | cut -f2- -d' ')" 2>/dev/null ||
  source "$0.runfiles/$f" 2>/dev/null ||
  source "$(grep -sm1 "^$f " "$0.runfiles_manifest" | cut -f2- -d' ')" 2>/dev/null ||
  source "$(grep -sm1 "^$f " "$0.exe.runfiles_manifest" | cut -f2- -d' ')" 2>/dev/null ||
  {
    echo >&2 "ERROR: cannot find $f"
    exit 1
  }
f=
set -e
# --- end runfiles.bash initialization v3 ---

scanner_rlocation="$1"
shift

scanner="$(rlocation "$scanner_rlocation")"
[[ -x "$scanner" ]] || { echo >&2 "ERROR: sonar-scanner not executable at '$scanner'"; exit 1; }

: "${BUILD_WORKSPACE_DIRECTORY:?must be run via 'bazel run', not executed directly}"
cd "$BUILD_WORKSPACE_DIRECTORY"

# Require explicit credentials to prevent an anonymous scan of a default host.
: "${SONAR_HOST_URL:?SONAR_HOST_URL is unset; export it before scanning}"
: "${SONAR_TOKEN:?SONAR_TOKEN is unset; export it before scanning}"

exec "$scanner" "-Dsonar.host.url=${SONAR_HOST_URL}" "-Dsonar.token=${SONAR_TOKEN}" "$@"
