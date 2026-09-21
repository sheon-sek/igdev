#!/usr/bin/env bash
set -euo pipefail

: "${GATEWAY_URL:?GATEWAY_URL is required}"

# Foundation smoke check: the Gateway HTTP endpoint must respond.
curl --fail --silent --show-error --location --output /dev/null "${GATEWAY_URL}/"
printf 'Gateway smoke check passed: %s\n' "$GATEWAY_URL"

# Add project-specific MCP/OpenAPI assertions below when this foundation is vendored
# into an application repository.
