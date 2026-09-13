#!/usr/bin/env bash
# Run the hermetic sonar-scanner against the workspace.
#
# Invoked as `bazel run //tools/sonar:scan -- [extra scanner args]`. $1 is the
# rlocationpath of the scanner launcher, injected by the sh_binary's `args` (see
# BUILD.bazel) so we never have to hand-mangle the bzlmod canonical repo name.
#
# The scan must run against the SOURCE tree (sonar-project.properties points at
# target/sonar/go-coverage.out and the real source paths), not the runfiles
# sandbox -- so we cd to BUILD_WORKSPACE_DIRECTORY, which `bazel run` sets.
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

# Creds: CI supplies SONAR_HOST_URL/SONAR_TOKEN from secrets. Fail loud rather
# than let the scanner run against a default/anonymous endpoint.
: "${SONAR_HOST_URL:?SONAR_HOST_URL is unset -- export it (CI: from secrets)}"
: "${SONAR_TOKEN:?SONAR_TOKEN is unset -- export it (CI: from secrets)}"

exec "$scanner" "-Dsonar.host.url=${SONAR_HOST_URL}" "-Dsonar.token=${SONAR_TOKEN}" "$@"
