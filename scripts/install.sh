#!/bin/sh
# Install or update the nau binary on macOS and Linux, from a ref.
# Re-running the script with another --ref updates the node to that ref.
# Pin an automatic backend selection to its absolute path on each install.
#
# On macOS, --agent installs (or rewrites) the nau agent LaunchAgent and
# (re)starts it; the service is created only when hub.json exists. A
# re-run without --agent restarts the service when the plist already
# exists, so the machine never runs the old binary.
#
# Prerequisites: Go 1.26 or newer on the PATH. The script never prints
# environment variables or configuration contents.

set -eu

REF="main"
BIN_DIR="$HOME/.local/bin"
MACHINE_LABEL=""
MODULE_PATH="github.com/djalmajr/nau/cmd/nau"
AGENT_LABEL="djalmajr.nau.agent"
AGENT_PLIST="$HOME/Library/LaunchAgents/$AGENT_LABEL.plist"
AGENT_LOG="$HOME/Library/Logs/nau-agent.log"
AGENT=0
AGENT_DRY_RUN=0
REMOVE_AGENT=0
AGENT_SINCE=""

usage() {
    cat <<'EOF'
Usage: sh scripts/install.sh [options]

Install or update the nau binary into the bin directory, from the ref.
Updating is re-running the script with another --ref.
On macOS, --agent installs the nau agent LaunchAgent and (re)starts it;
it is created only when the config directory holds a hub.json.
An automatic backend selection is pinned to its absolute path on the PATH.

Options:
  --ref <ref>            ref to install (default: main)
  --bin-dir <dir>        bin directory (default: $HOME/.local/bin)
  --machine-label <lbl>  set machine_label when it is still empty
  --agent                install or rewrite the nau agent LaunchAgent and (re)start it (macOS)
  --remove-agent         stop and remove the nau agent LaunchAgent (macOS)
  --agent-dry-run        print the LaunchAgent plist that --agent would install and exit (macOS)
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
    echo "nau install: warning: soho not found on the PATH; herdr_soho_bin stays automatic" >&2
}

# The bin directory as an absolute path, for the plist arguments: if
# it does not start with /, prefix it with the working directory, so
# a relative --bin-dir never becomes a relative ProgramArguments.
abs_bin_dir() {
    case "$BIN_DIR" in
    /*)
        printf '%s\n' "$BIN_DIR"
        ;;
    *)
        printf '%s/%s\n' "$(pwd)" "$BIN_DIR"
        ;;
    esac
}

# Escape the five XML special characters, so any path value is safe in
# the plist.
xml_escape() {
    printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g' -e "s/'/\&apos;/g"
}

# Append an entry to the service PATH, in order and without
# duplication.
AGENT_PATH_ENTRIES=""
agent_add_path_entry() {
    [ -n "$1" ] || return 0
    case ":$AGENT_PATH_ENTRIES:" in
    *":$1:"*) ;;
    *) AGENT_PATH_ENTRIES="${AGENT_PATH_ENTRIES:+$AGENT_PATH_ENTRIES:}$1" ;;
    esac
}

# A directory as an absolute path: command -v answers with a relative
# pathname when the PATH entry is relative, and the service starts
# from another cwd. Keep navigation preferences and output out of PATH.
agent_abs_dir() {
    (CDPATH=; export CDPATH; cd "$1" >/dev/null 2>&1 && pwd)
}

# Build the service PATH: the bin directory, the soho directory and the
# herdr directory (both resolved on the PATH at install time, as
# absolute paths) and the system directories, in that order and without
# duplication. A missing soho or herdr only warns: the agent runs and
# reports what is missing.
agent_build_service_path() {
    AGENT_PATH_ENTRIES=""
    agent_add_path_entry "$ABS_BIN_DIR"
    if AGENT_SOHO_BIN="$(command -v soho)" &&
        AGENT_SOHO_DIR="$(agent_abs_dir "$(dirname -- "$AGENT_SOHO_BIN")")"; then
        agent_add_path_entry "$AGENT_SOHO_DIR"
    else
        echo "nau install: warning: soho not found on the PATH; the agent will report soho_unavailable" >&2
    fi
    if AGENT_HERDR_BIN="$(command -v herdr)" &&
        AGENT_HERDR_DIR="$(agent_abs_dir "$(dirname -- "$AGENT_HERDR_BIN")")"; then
        agent_add_path_entry "$AGENT_HERDR_DIR"
    else
        echo "nau install: warning: herdr not found on the PATH; the agent may report soho_unavailable" >&2
    fi
    agent_add_path_entry /usr/bin
    agent_add_path_entry /bin
    agent_add_path_entry /usr/sbin
    agent_add_path_entry /sbin
}

# Print the LaunchAgent plist to stdout: nothing is written or started
# (this is also the --agent-dry-run output).
agent_print_plist() {
    agent_build_service_path
    esc_label="$(xml_escape "$AGENT_LABEL")"
    esc_nau="$(xml_escape "$ABS_BIN_DIR/nau")"
    esc_log="$(xml_escape "$AGENT_LOG")"
    esc_path="$(xml_escape "$AGENT_PATH_ENTRIES")"
    envvars="<key>PATH</key>
<string>$esc_path</string>"
    # XDG_CONFIG_HOME is carried into the service when it is set, so the
    # service reads the same config directory as the installer; without
    # it the plist keeps the classic shape.
    if [ -n "${XDG_CONFIG_HOME:-}" ]; then
        envvars="$envvars
<key>XDG_CONFIG_HOME</key>
<string>$(xml_escape "$XDG_CONFIG_HOME")</string>"
    fi
    cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
<key>Label</key>
<string>$esc_label</string>
<key>ProgramArguments</key>
<array>
<string>$esc_nau</string>
<string>agent</string>
<string>run</string>
</array>
<key>RunAtLoad</key>
<true/>
<key>KeepAlive</key>
<true/>
<key>ThrottleInterval</key>
<integer>10</integer>
<key>StandardOutPath</key>
<string>$esc_log</string>
<key>StandardErrorPath</key>
<string>$esc_log</string>
<key>EnvironmentVariables</key>
<dict>
$envvars
</dict>
</dict>
</plist>
EOF
}

# The nau directory under a config root, without a doubled slash, so two
# spellings of one root compare equal.
nau_dir_under() {
    root="$1"
    while [ "${root%/}" != "$root" ]; do
        root="${root%/}"
    done
    printf '%s/nau\n' "$root"
}

# The config directory the installed service reads: the XDG_CONFIG_HOME
# in its plist, or else ~/.config, whatever this run has in its
# environment. plutil is the one of the system, whatever the PATH of
# this run holds; it fails only on a plist without that key or that
# launchd could not load either.
agent_service_config_dir() {
    if service_xdg="$(/usr/bin/plutil -extract EnvironmentVariables.XDG_CONFIG_HOME raw -o - "$AGENT_PLIST" 2>/dev/null)" && [ -n "$service_xdg" ]; then
        nau_dir_under "$service_xdg"
    else
        printf '%s/.config/nau\n' "$HOME"
    fi
}

# Wait for the agent to write a fresh health file under a wall deadline
# of 20 s. The agent writes the file before its first read, so a file
# newer than the restart moment proves the service started; the check
# is a plain file test, so nothing can block past the deadline.
agent_health_check() {
    deadline=$(( $(date +%s) + 20 ))
    while :; do
        if [ -f "$AGENT_STATE" ] && [ "$AGENT_STATE" -nt "$AGENT_SINCE" ]; then
            echo "nau install: the agent service started; run nau doctor to check delivery" >&2
            return 0
        fi
        [ "$(date +%s)" -lt "$deadline" ] || break
        sleep 0.5
    done
    echo "nau install: the agent did not write a fresh $AGENT_STATE; see $AGENT_LOG" >&2
    exit 1
}

# (Re)start the agent LaunchAgent and wait for its fresh health file.
# $1 is 1 when the plist was rewritten with different content: a
# loaded job is then bootout and bootstrapped, otherwise it is
# kickstarted; an unloaded job is bootstrapped.
agent_restart_service() {
    changed="$1"
    uid="$(id -u)"
    loaded=0
    if launchctl print "gui/$uid/$AGENT_LABEL" >/dev/null 2>&1; then
        loaded=1
    fi
    # The restart moment: the health file is fresh when it is newer
    # than this file.
    AGENT_SINCE="$(mktemp)"
    if [ "$loaded" -eq 1 ] && [ "$changed" -eq 1 ]; then
        launchctl bootout "gui/$uid/$AGENT_LABEL"
        launchctl bootstrap "gui/$uid" "$AGENT_PLIST"
    elif [ "$loaded" -eq 1 ]; then
        launchctl kickstart -k "gui/$uid/$AGENT_LABEL"
    else
        launchctl bootstrap "gui/$uid" "$AGENT_PLIST"
    fi
    agent_health_check
}

# Write the plist (temp file + rename, mode 0600), create the log
# directory and (re)start the service.
agent_install_service() {
    mkdir -p "$(dirname -- "$AGENT_PLIST")"
    mkdir -p "$(dirname -- "$AGENT_LOG")"
    STAGE="$AGENT_PLIST.tmp.$$"
    agent_print_plist > "$STAGE"
    chmod 600 "$STAGE"
    changed=1
    if [ -f "$AGENT_PLIST" ] && cmp -s "$STAGE" "$AGENT_PLIST"; then
        changed=0
    fi
    mv "$STAGE" "$AGENT_PLIST"
    STAGE=""
    agent_restart_service "$changed"
}

# Stop and remove the agent LaunchAgent, and only it: a job that is
# not loaded is skipped, and a missing plist is a no-op.
agent_remove_service() {
    uid="$(id -u)"
    if launchctl print "gui/$uid/$AGENT_LABEL" >/dev/null 2>&1; then
        launchctl bootout "gui/$uid/$AGENT_LABEL"
    fi
    rm -f "$AGENT_PLIST"
    echo "nau install: agent service removed" >&2
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
    --agent)
        AGENT=1
        shift
        ;;
    --remove-agent)
        REMOVE_AGENT=1
        shift
        ;;
    --agent-dry-run)
        AGENT_DRY_RUN=1
        shift
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

OS="$(uname -s)"

# The config directory the agent command reads hub.json from (the same
# directory the nau binary resolves): XDG or ~/.config.
if [ -n "${XDG_CONFIG_HOME:-}" ]; then
    NAU_CONFIG_DIR="$(nau_dir_under "$XDG_CONFIG_HOME")"
else
    NAU_CONFIG_DIR="$HOME/.config/nau"
fi
HUB_FILE="$NAU_CONFIG_DIR/hub.json"
AGENT_STATE="$NAU_CONFIG_DIR/agent-state.json"

# The service options are refused before any step (before the go
# check, before the install), on every non-macOS platform; on a
# platform without the plist, the automatic restart never happens.
if { [ "$AGENT" -eq 1 ] || [ "$AGENT_DRY_RUN" -eq 1 ] || [ "$REMOVE_AGENT" -eq 1 ]; } && [ "$OS" != "Darwin" ]; then
    echo "nau install: --agent is supported on macOS and Windows only" >&2
    exit 2
fi

# XDG_CONFIG_HOME is carried into the service environment, so a
# relative value would point the service at a different directory;
# refuse it before any step.
if { [ "$AGENT" -eq 1 ] || [ "$AGENT_DRY_RUN" -eq 1 ]; } && [ -n "${XDG_CONFIG_HOME:-}" ]; then
    case "${XDG_CONFIG_HOME:-}" in
    /*) ;;
    *)
        echo "nau install: XDG_CONFIG_HOME must be an absolute path" >&2
        exit 2
        ;;
    esac
fi

# --agent-dry-run prints the plist and exits 0 before the go install,
# without touching anything (it wins over --agent when both are given).
# Without hub.json it prints the same refusal --agent would take.
if [ "$AGENT_DRY_RUN" -eq 1 ]; then
    if [ -f "$HUB_FILE" ]; then
        ABS_BIN_DIR="$(abs_bin_dir)"
        agent_print_plist
    else
        echo "nau install: no hub.json in $HUB_FILE; run nau connect <hub-url> first; the agent service would not be installed" >&2
    fi
    exit 0
fi

# --remove-agent stops and removes the service and exits, without
# installing anything.
if [ "$REMOVE_AGENT" -eq 1 ]; then
    agent_remove_service
    exit 0
fi

if ! command -v go >/dev/null 2>&1; then
    echo "nau install: go not found on the PATH; install Go 1.26 or newer first" >&2
    exit 1
fi

# A dedicated temp dir, removed on any exit: the fresh binary is built
# there before the installed one is touched.
TMP="$(mktemp -d)"
STAGE=""
trap 'rm -rf "$TMP"; if [ -n "$STAGE" ]; then rm -f "$STAGE"; fi; if [ -n "$AGENT_SINCE" ]; then rm -f "$AGENT_SINCE"; fi' EXIT

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

# nau agent service (macOS only): with --agent, install (or rewrite)
# the LaunchAgent and (re)start it, but only when the config directory
# holds a hub.json; without it, warn to run nau connect first and keep
# the node usable. Without --agent, an existing plist is restarted over
# the fresh binary, so the machine never runs the old binary: the wait
# for the fresh health file (20 s) applies to that restart as well.
# This only happens after a successful swap: a failed go install never
# touches the service.
if [ "$OS" = "Darwin" ] && [ "$AGENT" -eq 1 ]; then
    if [ ! -f "$HUB_FILE" ]; then
        echo "nau install: no hub.json in $HUB_FILE; run nau connect <hub-url> first; the agent service was not installed" >&2
    else
        ABS_BIN_DIR="$(abs_bin_dir)"
        agent_install_service
    fi
elif [ "$OS" = "Darwin" ] && [ -f "$AGENT_PLIST" ]; then
    # The plist stays as it is, so the service keeps reading its own
    # directory: the health file to wait for is the one in there, even
    # when XDG_CONFIG_HOME changed after the service was installed.
    SERVICE_CONFIG_DIR="$(agent_service_config_dir)"
    AGENT_STATE="$SERVICE_CONFIG_DIR/agent-state.json"
    if [ "$SERVICE_CONFIG_DIR" != "$NAU_CONFIG_DIR" ]; then
        echo "nau install: warning: the agent service reads $SERVICE_CONFIG_DIR, not $NAU_CONFIG_DIR; run the installer with --agent to move it" >&2
    fi
    agent_restart_service 0
fi

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
# doctor is informational: its output must not fail the install.
if ! "$NAU" doctor; then
    echo "nau install: doctor reported a problem (informational); see the output above" >&2
fi
