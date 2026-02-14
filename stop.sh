#!/usr/bin/env bash
set -euo pipefail

CONTAINER_NAME="mihomo-monitor"

if ! docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER_NAME"; then
  echo "Container $CONTAINER_NAME does not exist"
  exit 0
fi

echo "Stopping $CONTAINER_NAME"
docker stop "$CONTAINER_NAME"
docker rm "$CONTAINER_NAME"
echo "Container $CONTAINER_NAME stopped and removed"
