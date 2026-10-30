#!/bin/sh
# Install or update the nau binary on macOS and Linux, from a ref.
# Re-running the script with another --ref updates the node to that ref.
# Pin an automatic backend selection to its absolute path on each install.
#
# Prerequisites: Go 1.26 or newer on the PATH. The script never prints
# environment variables or configuration contents.

set -eu

REF="main"
BIN_DIR="$HOME/.local/bin"
MACHINE_LABEL=""
MODULE_PATH="github.com/djalmajr/nau/cmd/nau"

usage() {
    cat <<'EOF'
Usage: sh scripts/install.sh [options]

Install or update the nau binary into the bin directory, from the ref.
Updating is re-running the script with another --ref.
An automatic backend selection is pinned to its absolute path on the PATH.

Options:
  --ref <ref>            ref to install (default: main)
  --bin-dir <dir>        bin directory (default: $HOME/.local/bin)
  --machine-label <lbl>  set machine_label when it is still empty
  -h, --help             show this help
EOF
}

pin_backend() {
    # Go owns classification, path safety and the locked conditional write.
    if ! "$NAU" config list >/dev/null 2>&1; then
        echo "nau install: warning: cannot read the config file; herdr_soho_bin unchanged" >&2
        return
    fi
    BACKEND_PATH="$(command -v soho 2>/dev/null)" || BACKEND_PATH=""
    case "$BACKEND_PATH" in
    /*)
        if ! "$NAU" config set --if-automatic herdr_soho_bin "$BACKEND_PATH" >/dev/null; then
            echo "nau install: nau config set herdr_soho_bin failed" >&2
            exit 1
        fi
        return
        ;;
    esac
    echo "nau install: warning: soho not found on the PATH; herdr_soho_bin stays automatic, and an ssh forced command (nau gate) may not find the backend" >&2
}

while [ "$#" -gt 0 ]; do
    case "$1" in
    --ref)
        if [ "$#" -lt 2 ]; then
            echo "nau install: --ref requires a value" >&2
            exit 2
        fi
        REF="$2"
        shift 2
        ;;
    --bin-dir)
        if [ "$#" -lt 2 ]; then
            echo "nau install: --bin-dir requires a value" >&2
            exit 2
        fi
        BIN_DIR="$2"
        shift 2
        ;;
    --machine-label)
        if [ "$#" -lt 2 ]; then
            echo "nau install: --machine-label requires a value" >&2
            exit 2
        fi
        MACHINE_LABEL="$2"
        shift 2
        ;;
    -h | --help)
        usage
        exit 0
        ;;
    *)
        echo "nau install: unknown option: $1" >&2
        usage >&2
        exit 2
        ;;
    esac
done

if ! command -v go >/dev/null 2>&1; then
    echo "nau install: go not found on the PATH; install Go 1.26 or newer first" >&2
    exit 1
fi

# A dedicated temp dir, removed on any exit: the fresh binary is built
# there before the installed one is touched.
TMP="$(mktemp -d)"
STAGE=""
trap 'rm -rf "$TMP"; if [ -n "$STAGE" ]; then rm -f "$STAGE"; fi' EXIT

echo "nau install: installing $MODULE_PATH@$REF"
GOBIN="$TMP" GOPROXY=direct go install "$MODULE_PATH@$REF"

# Test the new binary before touching the installed one.
echo "nau install: checking the new binary"
if ! "$TMP/nau" version >/dev/null; then
    echo "nau install: the new binary failed 'nau version'; the installed binary is untouched" >&2
    exit 1
fi

# Replace the installed binary: copy to a sibling temp name and rename
# over it, so the swap is atomic within the same directory.
mkdir -p "$BIN_DIR"
STAGE="$BIN_DIR/.nau.new.$$"
cp "$TMP/nau" "$STAGE"
mv "$STAGE" "$BIN_DIR/nau"
STAGE=""
NAU="$BIN_DIR/nau"

# Set machine_label only when it is still empty; never print the value.
if [ -n "$MACHINE_LABEL" ]; then
    CURRENT="$("$NAU" config get machine_label 2>/dev/null)" || CURRENT=""
    CURRENT_VALUE="$(printf '%s\n' "$CURRENT" | sed -n 's/.*"value":"\([^"]*\)".*/\1/p')"
    if [ -z "$CURRENT_VALUE" ]; then
        "$NAU" config set machine_label "$MACHINE_LABEL" >/dev/null
        echo "nau install: machine_label set" >&2
    else
        echo "nau install: machine_label is already set; keeping the current value" >&2
    fi
fi

pin_backend

# Warn, without failing, when the bin dir is not on the PATH.
case ":$PATH:" in
*":$BIN_DIR:"*) ;;
*)
    echo "nau install: warning: $BIN_DIR is not on the PATH; add it to use nau" >&2
    ;;
esac

echo "nau install: installed" >&2
"$NAU" version
# doctor is informational: its non-zero exit (for example a missing
# backend) must not fail the install.
if ! "$NAU" doctor; then
    echo "nau install: doctor reported a problem (informational); see the output above" >&2
fi
