#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME="mihomo-monitor.service"
BIN_NAME="mihomo-monitor"
INSTALL_BIN_PATH="/usr/local/bin/${BIN_NAME}"
SYSTEMD_UNIT_PATH="/etc/systemd/system/${SERVICE_NAME}"
ENV_TARGET_PATH="/etc/mihomo-monitor.env"
STATE_DIR="/var/lib/mihomo-monitor"

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
UNIT_SOURCE_PATH="${SCRIPT_DIR}/${SERVICE_NAME}"
ENV_SOURCE_PATH="${SCRIPT_DIR}/_env_example"
BUILD_OUTPUT_PATH="${SCRIPT_DIR}/${BIN_NAME}"

run_root() {
  if [[ "${EUID}" -eq 0 ]]; then
    "$@"
    return
  fi

  if command -v sudo >/dev/null 2>&1; then
    sudo "$@"
    return
  fi

  echo "Error: root privileges are required (run as root or install sudo)." >&2
  exit 1
}

if ! command -v go >/dev/null 2>&1; then
  echo "Error: go is not installed." >&2
  exit 1
fi

if [[ ! -f "${UNIT_SOURCE_PATH}" ]]; then
  echo "Error: systemd unit template not found: ${UNIT_SOURCE_PATH}" >&2
  exit 1
fi

echo "[1/6] Building ${BIN_NAME}"
go build -o "${BUILD_OUTPUT_PATH}" "${SCRIPT_DIR}"

echo "[2/6] Installing binary to ${INSTALL_BIN_PATH}"
run_root install -m 0755 "${BUILD_OUTPUT_PATH}" "${INSTALL_BIN_PATH}"

echo "[3/6] Installing systemd unit to ${SYSTEMD_UNIT_PATH}"
run_root install -m 0644 "${UNIT_SOURCE_PATH}" "${SYSTEMD_UNIT_PATH}"

echo "[4/6] Ensuring state directory ${STATE_DIR}"
run_root mkdir -p "${STATE_DIR}"

if [[ ! -f "${ENV_TARGET_PATH}" ]]; then
  if [[ -f "${ENV_SOURCE_PATH}" ]]; then
    echo "[5/6] Installing default env file to ${ENV_TARGET_PATH}"
    run_root install -m 0644 "${ENV_SOURCE_PATH}" "${ENV_TARGET_PATH}"
  else
    echo "[5/6] Skipping env file install (_env_example not found)"
  fi
else
  echo "[5/6] Keeping existing env file ${ENV_TARGET_PATH}"
fi

echo "[6/6] Reloading systemd and enabling service"
run_root systemctl daemon-reload
run_root systemctl enable --now "${SERVICE_NAME}"

echo
echo "Installed ${SERVICE_NAME}."
echo "Check status: systemctl status ${SERVICE_NAME}"
echo "Edit config:  ${ENV_TARGET_PATH}"
echo "Restart:      systemctl restart ${SERVICE_NAME}"
