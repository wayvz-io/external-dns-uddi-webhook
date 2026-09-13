#!/usr/bin/env bash
set -euo pipefail

# Buildifier strict lint check (formatting + linting) for external-dns-uddi-webhook.

echo "Running strict buildifier checks (formatting + linting)..."

# Resolve the buildifier binary. Prefer a hermetic binary passed as $1 (the
# Bazel test/binary rule supplies @buildifier_prebuilt//:buildifier), so this
# runs on a bare runner without nix. Resolve to an absolute path now, before
# any cd below changes the working directory. Fall back to PATH (nix dev-shell).
BUILDIFIER=""
if [ "${1:-}" != "" ] && [ -e "${1}" ]; then
    BUILDIFIER="$(cd "$(dirname "${1}")" && pwd)/$(basename "${1}")"
fi

# Find the source directory (when running from Bazel runfiles)
if [[ $(pwd) == *"runfiles"* ]]; then
    echo "Running from Bazel runfiles, looking for source directory..."
    if [ -n "${BUILD_WORKSPACE_DIRECTORY:-}" ]; then
        cd "$BUILD_WORKSPACE_DIRECTORY"
        echo "Using BUILD_WORKSPACE_DIRECTORY: $(pwd)"
    else
        # In test environment, run buildifier on the runfiles directory, which
        # contains the BUILD and .bzl files as test data.
        echo "Running buildifier on test data files in current directory..."
    fi
fi

# Prefer a buildifier already on PATH (e.g. the nix dev-shell binary, which runs
# natively on the host -- including NixOS). Otherwise use the hermetic binary
# resolved above, which is what a bare runner / CI without a host buildifier uses.
if command -v buildifier &> /dev/null; then
    BUILDIFIER="buildifier"
elif [ -n "$BUILDIFIER" ]; then
    : # use the hermetic binary passed as $1 (already resolved to an absolute path)
else
    echo "ERROR: buildifier not found (no hermetic binary passed and none in PATH)"
    echo "Run via 'bazel test //:buildifier_test' or from a nix environment with 'nix develop'"
    exit 1
fi

TEMP_OUTPUT=$(mktemp)
trap 'rm -f "$TEMP_OUTPUT"' EXIT

set +e  # Don't exit on buildifier warnings
"$BUILDIFIER" -lint=warn -mode=check -r . > "$TEMP_OUTPUT" 2>&1
BUILDIFIER_EXIT_CODE=$?
set -e

if grep -q "# reformat" "$TEMP_OUTPUT"; then
    echo "Files need reformatting:"
    grep "# reformat" "$TEMP_OUTPUT"
    echo ""
    echo "Run: buildifier -mode=fix -r ."
    exit 1
fi

if grep -q -E ": (unused-variable|print)" "$TEMP_OUTPUT"; then
    echo "Critical linting issues found:"
    grep -E ": (unused-variable|print)" "$TEMP_OUTPUT" | head -20
    exit 1
fi

if grep -q -E ": (function-docstring|module-docstring)" "$TEMP_OUTPUT"; then
    echo "Documentation issues found:"
    grep -E ": (function-docstring|module-docstring)" "$TEMP_OUTPUT" | head -10
    exit 1
fi

if [ $BUILDIFIER_EXIT_CODE -ne 0 ]; then
    echo "Buildifier failed with exit code $BUILDIFIER_EXIT_CODE"
    cat "$TEMP_OUTPUT"
    exit $BUILDIFIER_EXIT_CODE
fi

echo "All BUILD files pass strict formatting and linting checks"
