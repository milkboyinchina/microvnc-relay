#!/usr/bin/env bash
# install-systemd.sh — Installs microvnc-relay as a systemd service on Debian/Ubuntu/Raspberry Pi OS
set -euo pipefail

BIN_SRC="${1:-./microvnc-relay-linux-armv7}"
CONFIG_SRC="${2:-./config.example.yaml}"

echo "==> Installing μVNC Relay systemd service..."

if [[ $EUID -ne 0 ]]; then
   echo "Error: This script must be run as root (e.g. sudo bash scripts/install-systemd.sh)" >&2
   exit 1
fi

if [[ ! -f "$BIN_SRC" ]]; then
    # Try local native build if binary not passed
    if [[ -f "./microvnc-relay" ]]; then
        BIN_SRC="./microvnc-relay"
    else
        echo "Error: Binary not found at $BIN_SRC" >&2
        exit 1
    fi
fi

# 1. Install binary
echo "==> Copying binary to /usr/local/bin/microvnc-relay..."
install -m 0755 "$BIN_SRC" /usr/local/bin/microvnc-relay

# 2. Setup configuration directory and initial config
mkdir -p /etc/microvnc-relay
if [[ ! -f /etc/microvnc-relay/config.yaml ]]; then
    echo "==> Creating default /etc/microvnc-relay/config.yaml from $CONFIG_SRC..."
    cp "$CONFIG_SRC" /etc/microvnc-relay/config.yaml
    chmod 0640 /etc/microvnc-relay/config.yaml
else
    echo "==> Preserving existing /etc/microvnc-relay/config.yaml"
fi

# 3. Install systemd service unit
echo "==> Installing systemd unit file..."
install -m 0644 packaging/systemd/microvnc-relay.service /etc/systemd/system/microvnc-relay.service

# 4. Reload and enable
systemctl daemon-reload
systemctl enable microvnc-relay.service

echo "==> Installation complete! To start μVNC Relay now, run:"
echo "    sudo systemctl start microvnc-relay"
echo "==> To inspect live logs:"
echo "    journalctl -u microvnc-relay -f"
