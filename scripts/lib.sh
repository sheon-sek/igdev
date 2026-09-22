#!/usr/bin/env bash
# Shared runtime/config helpers. Sourced by devctl.

RUNTIME_DIR="$ROOT_DIR/.runtime"
RUNTIME_ENV="$RUNTIME_DIR/compose.env"
MODULE_STAGE_DIR="$RUNTIME_DIR/docker/modules"
PRIVATE_MODULE_DIR="$ROOT_DIR/modules/private"
RESTORE_DIR="$RUNTIME_DIR/restore"

log() { printf '[devctl] %s\n' "$*"; }
warn() { printf '[devctl] WARNING: %s\n' "$*" >&2; }
die() { printf '[devctl] ERROR: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }
need() { have "$1" || die "Missing prerequisite: $1"; }

load_config() {
  # Repository defaults.
  # shellcheck disable=SC1091
  source "$ROOT_DIR/config/versions.env"
  IGNITION_VERSION="$DEFAULT_IGNITION_VERSION"
  JYTHON_VERSION="$DEFAULT_JYTHON_VERSION"
  GATEWAY_BIND_ADDRESS=127.0.0.1
  GATEWAY_HTTP_PORT=auto
  GATEWAY_HTTPS_PORT=auto
  GATEWAY_DEBUG_PORT=auto
  GATEWAY_NAME=auto
  GATEWAY_MAX_MEMORY_MB=2048
  GATEWAY_ADMIN_USERNAME=admin
  GATEWAY_ADMIN_PASSWORD=change-me-dev-only
  IGNITION_EDITION=standard
  TZ=UTC
  ACCEPT_IGNITION_EULA=N
  GATEWAY_MODULES_ENABLED=''
  ACCEPT_MODULE_CERTS=''
  ACCEPT_MODULE_LICENSES=''
  IGNITION_ALLOW_UNSIGNED_MODULES=false
  MODULE_CACHE_ROOT="${HOME}/.cache/ignition-devenv/modules"
  GATEWAY_BACKUP=''
  PROJECT_CHECK_CMD=''
  PROJECT_TEST_CMD=''
  PROJECT_BUILD_CMD=''
  PROJECT_MODULE_GLOB=''
  JYTHON_SOURCE_PATHS=''
  MODULE_REQUIREMENT_PATHS=''
  VERIFY_GATEWAY=0
  ACT_OFFLINE=0

  local cfg="$ROOT_DIR/.env"
  [[ -f "$cfg" ]] || cfg="$ROOT_DIR/.env.example"
  if [[ -f "$cfg" ]]; then
    set -a
    # shellcheck disable=SC1090
    source "$cfg"
    set +a
  fi
  MODULE_CACHE_ROOT="${MODULE_CACHE_ROOT/#\~/$HOME}"
}

instance_number() { printf '%s' "$ROOT_DIR" | cksum | awk '{print $1}'; }
instance_id() { printf '%08x' "$(( $(instance_number) % 4294967295 ))"; }

resolve_runtime() {
  load_config
  DEV_INSTANCE_ID="$(instance_id)"
  COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-ignition-dev-${DEV_INSTANCE_ID}}"
  local n offset
  n="$(instance_number)"; offset=$((n % 700))
  [[ "$GATEWAY_HTTP_PORT" == auto ]] && GATEWAY_HTTP_PORT=$((18000 + offset))
  [[ "$GATEWAY_HTTPS_PORT" == auto ]] && GATEWAY_HTTPS_PORT=$((19000 + offset))
  [[ "$GATEWAY_DEBUG_PORT" == auto ]] && GATEWAY_DEBUG_PORT=$((20000 + offset))
  [[ "$GATEWAY_NAME" == auto ]] && GATEWAY_NAME="ignition-dev-${DEV_INSTANCE_ID}"
  if [[ -f "$RESTORE_DIR/restore.gwbk" ]]; then
    GATEWAY_RESTORE_ARGS='-r /restore/restore.gwbk'
  else
    GATEWAY_RESTORE_ARGS=''
  fi
}

write_runtime_env() {
  resolve_runtime
  mkdir -p "$RUNTIME_DIR" "$RESTORE_DIR"
  cat > "$RUNTIME_ENV" <<EOF_ENV
IGNITION_VERSION=$IGNITION_VERSION
JYTHON_VERSION=$JYTHON_VERSION
DEV_INSTANCE_ID=$DEV_INSTANCE_ID
COMPOSE_PROJECT_NAME=$COMPOSE_PROJECT_NAME
GATEWAY_BIND_ADDRESS=$GATEWAY_BIND_ADDRESS
GATEWAY_HTTP_PORT=$GATEWAY_HTTP_PORT
GATEWAY_HTTPS_PORT=$GATEWAY_HTTPS_PORT
GATEWAY_DEBUG_PORT=$GATEWAY_DEBUG_PORT
GATEWAY_NAME=$GATEWAY_NAME
GATEWAY_MAX_MEMORY_MB=$GATEWAY_MAX_MEMORY_MB
GATEWAY_ADMIN_USERNAME=$GATEWAY_ADMIN_USERNAME
GATEWAY_ADMIN_PASSWORD=$GATEWAY_ADMIN_PASSWORD
IGNITION_EDITION=$IGNITION_EDITION
TZ=$TZ
ACCEPT_IGNITION_EULA=$ACCEPT_IGNITION_EULA
GATEWAY_MODULES_ENABLED=$GATEWAY_MODULES_ENABLED
ACCEPT_MODULE_CERTS=$ACCEPT_MODULE_CERTS
ACCEPT_MODULE_LICENSES=$ACCEPT_MODULE_LICENSES
IGNITION_ALLOW_UNSIGNED_MODULES=$IGNITION_ALLOW_UNSIGNED_MODULES
GATEWAY_RESTORE_ARGS=$GATEWAY_RESTORE_ARGS
EOF_ENV
}

compose() {
  write_runtime_env
  docker compose --env-file "$RUNTIME_ENV" -p "$COMPOSE_PROJECT_NAME" -f "$ROOT_DIR/docker-compose.yml" "$@"
}

gateway_url() {
  resolve_runtime
  printf 'http://%s:%s\n' "$GATEWAY_BIND_ADDRESS" "$GATEWAY_HTTP_PORT"
}

module_cache_path() {
  load_config
  printf '%s/%s\n' "$MODULE_CACHE_ROOT" "$IGNITION_VERSION"
}

stage_modules() {
  load_config
  mkdir -p "$MODULE_STAGE_DIR" "$PRIVATE_MODULE_DIR"
  find "$MODULE_STAGE_DIR" -maxdepth 1 -type f -delete
  : > "$MODULE_STAGE_DIR/.stage-ready"
  local cache f count=0
  cache="$(module_cache_path)"; mkdir -p "$cache"
  shopt -s nullglob
  for f in "$cache"/*.modl "$PRIVATE_MODULE_DIR"/*.modl; do cp -f "$f" "$MODULE_STAGE_DIR/"; count=$((count+1)); done
  if [[ -n "$PROJECT_MODULE_GLOB" ]]; then
    pushd "$ROOT_DIR" >/dev/null
    # PROJECT_MODULE_GLOB is a trusted local glob configured by the repository owner.
    # shellcheck disable=SC2086
    for f in $PROJECT_MODULE_GLOB; do [[ -f "$f" ]] || continue; cp -f "$f" "$MODULE_STAGE_DIR/"; count=$((count+1)); done
    popd >/dev/null
  fi
  shopt -u nullglob
  log "Staged $count module file(s) for Ignition $IGNITION_VERSION"
}

stage_backup_from_config() {
  load_config
  mkdir -p "$RESTORE_DIR"
  if [[ -n "$GATEWAY_BACKUP" ]]; then
    [[ -f "$GATEWAY_BACKUP" ]] || die "GATEWAY_BACKUP does not exist: $GATEWAY_BACKUP"
    cp -f "$GATEWAY_BACKUP" "$RESTORE_DIR/restore.gwbk"
  fi
}

run_project_cmd() {
  local label="$1" command_text="$2"
  [[ -n "$command_text" ]] || return 0
  log "Running $label: $command_text"
  (cd "$ROOT_DIR" && bash -lc "$command_text")
}

jython_jar() {
  load_config
  printf '%s/tools/jython/jython-standalone-%s.jar\n' "$RUNTIME_DIR" "$JYTHON_VERSION"
}

ensure_jython() {
  load_config; need java
  local jar url
  jar="$(jython_jar)"; [[ -s "$jar" ]] && return 0
  mkdir -p "$(dirname "$jar")"
  url="${JYTHON_MAVEN_BASE}/${JYTHON_VERSION}/jython-standalone-${JYTHON_VERSION}.jar"
  log "Downloading Jython $JYTHON_VERSION standalone checker"
  if have curl; then curl --fail --location --retry 3 --output "$jar.tmp" "$url"
  elif have wget; then wget -O "$jar.tmp" "$url"
  else die 'curl or wget is required'; fi
  mv "$jar.tmp" "$jar"
}
