#!/bin/bash
# Post-install script for sakms-node RPM.
# Writes /etc/sakms-node/config.json with an empty apiKey (triggers pairing
# mode on first start), then enables and starts the systemd service.
# Server URL is read from SAKMS_SERVER_URL env if set; otherwise the user
# is prompted interactively. Node name works the same way via SAKMS_NODE_NAME,
# defaulting the prompt to the machine's hostname.

set -euo pipefail

CONFIG_DIR=/etc/sakms-node
CONFIG_FILE="$CONFIG_DIR/config.json"

# Only write config on a fresh install (no existing config.json).
if [ ! -f "$CONFIG_FILE" ]; then
    if [ -n "${SAKMS_SERVER_URL:-}" ]; then
        SERVER_URL="$SAKMS_SERVER_URL"
    else
        read -r -p "sakms server URL (e.g. https://sakms.example.com): " SERVER_URL
    fi
    if [ -n "${SAKMS_NODE_NAME:-}" ]; then
        NODE_NAME="$SAKMS_NODE_NAME"
    else
        DEFAULT_NODE_NAME="$(hostname)"
        read -r -p "Node name [$DEFAULT_NODE_NAME]: " NODE_NAME
        NODE_NAME="${NODE_NAME:-$DEFAULT_NODE_NAME}"
    fi
    mkdir -p "$CONFIG_DIR"
    cat > "$CONFIG_FILE" <<JSON
{
  "serverUrl": "$SERVER_URL",
  "apiKey": "",
  "nodeName": "$NODE_NAME"
}
JSON
    chmod 600 "$CONFIG_FILE"
fi

systemctl enable --now sakms-node.service
