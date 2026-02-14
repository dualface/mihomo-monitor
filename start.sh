#!/usr/bin/env bash
set -euo pipefail

IMAGE_NAME="mihomo-monitor"
CONTAINER_NAME="mihomo-monitor"
ENV_FILE=".env"
EXTRA_ARGS="${*:---monitor}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

if [ ! -f "$SCRIPT_DIR/$ENV_FILE" ]; then
  echo "Error: $ENV_FILE not found in $SCRIPT_DIR" >&2
  exit 1
fi

docker build -t "$IMAGE_NAME" "$SCRIPT_DIR"

if docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER_NAME"; then
  echo "Removing existing container $CONTAINER_NAME"
  docker rm -f "$CONTAINER_NAME" >/dev/null
fi

echo "Starting $CONTAINER_NAME"
docker run -d \
  --name "$CONTAINER_NAME" \
  --restart unless-stopped \
  --network host \
  --env-file "$SCRIPT_DIR/$ENV_FILE" \
  "$IMAGE_NAME" $EXTRA_ARGS

echo "Container $CONTAINER_NAME started"
docker logs --tail 5 "$CONTAINER_NAME"
