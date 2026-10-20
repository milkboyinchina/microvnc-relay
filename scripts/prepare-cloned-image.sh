#!/usr/bin/env bash
# prepare-cloned-image.sh — Prepare a cloned SD/eMMC image for a new relay.
#
# Run this on the cloned image BEFORE first boot, or on the running clone if it
# has not yet joined the fleet. It:
#   - regenerates /etc/machine-id and the D-Bus machine-id
#   - removes the copied WebUI credentials (auth.json)
#   - removes any future relay_id state file
#
# It deliberately keeps targets.yaml: target IPs may be site-specific, so review
# that file manually after cloning.
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
    echo "Error: run as root (sudo bash scripts/prepare-cloned-image.sh)" >&2
    exit 1
fi

if [[ "${1:-}" != "--yes" ]]; then
    echo "This will regenerate machine-id and delete /etc/microvnc-relay/auth.json."
    read -r -p "Type 'yes' to continue: " answer
    [[ "$answer" == "yes" ]] || { echo "Aborted."; exit 1; }
fi

if ! command -v systemd-machine-id-setup >/dev/null 2>&1; then
    echo "Error: systemd-machine-id-setup is required" >&2
    exit 1
fi

echo "==> Removing cloned machine-id files..."
rm -f /etc/machine-id /var/lib/dbus/machine-id

echo "==> Generating a new machine-id..."
systemd-machine-id-setup

echo "==> Removing copied WebUI credentials..."
rm -f /etc/microvnc-relay/auth.json

echo "==> Removing copied relay identity state (future Phase 2 file)..."
rm -f /etc/microvnc-relay/relay_id

echo "==> Flushing filesystem buffers..."
sync

cat <<'EOF'

Clone preparation complete.

Next steps:
  1. Review /etc/microvnc-relay/targets.yaml (site-specific IPs).
  2. Start the service and read the new first-run setup token:
       sudo systemctl start microvnc-relay
       sudo journalctl -u microvnc-relay -n 50 | grep -i 'setup token'
  3. Complete WebUI setup with that token.
  4. If the clone uses a different hostname, set it explicitly.
EOF
