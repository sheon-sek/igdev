#!/usr/bin/env bash
# User-facing command implementations. Sourced by devctl.

usage() {
  cat <<'USAGE'
Usage: ./devctl <command> [args]

Core:      doctor | bootstrap [--accept-eula] | versions | self-test
Checks:    check | test | build | verify | ci-local | jython-check <path> [...]
Modules:   module add <file.modl> | module list | module cache-path | module clear
Baseline:  baseline set <file.gwbk> | baseline clear | baseline status
Gateway:   gateway up | down [--volumes] | reset | restart | wait [--timeout SEC]
           gateway smoke | status | logs [args...] | url
USAGE
}

cmd_doctor() {
  local failed=0
  for c in bash docker java; do
    if have "$c"; then printf 'OK   %s\n' "$c"; else printf 'MISS %s\n' "$c"; failed=1; fi
  done
  if have docker && docker compose version >/dev/null 2>&1; then printf 'OK   docker compose\n'; else printf 'MISS docker compose v2\n'; failed=1; fi
  if have curl || have wget; then printf 'OK   downloader (curl/wget)\n'; else printf 'MISS curl or wget\n'; failed=1; fi
  if have act; then printf 'OK   act (optional)\n'; else printf 'INFO act not installed (only needed for ci-local)\n'; fi
  return "$failed"
}

accept_eula_in_env() {
  [[ -f "$ROOT_DIR/.env" ]] || cp "$ROOT_DIR/.env.example" "$ROOT_DIR/.env"
  if grep -q '^ACCEPT_IGNITION_EULA=' "$ROOT_DIR/.env"; then
    sed -i.bak 's/^ACCEPT_IGNITION_EULA=.*/ACCEPT_IGNITION_EULA=Y/' "$ROOT_DIR/.env" && rm -f "$ROOT_DIR/.env.bak"
  else
    printf '\nACCEPT_IGNITION_EULA=Y\n' >> "$ROOT_DIR/.env"
  fi
}

cmd_bootstrap() {
  [[ -f "$ROOT_DIR/.env" ]] || { cp "$ROOT_DIR/.env.example" "$ROOT_DIR/.env"; log 'Created .env from .env.example'; }
  [[ "${1:-}" == '--accept-eula' ]] && { accept_eula_in_env; log 'Recorded local Ignition EULA acceptance'; }
  mkdir -p "$RUNTIME_DIR" "$RESTORE_DIR" "$PRIVATE_MODULE_DIR" "$MODULE_STAGE_DIR"
  ensure_jython
  stage_modules
  stage_backup_from_config
  write_runtime_env
  cmd_versions
  log 'Bootstrap complete'
}

cmd_versions() {
  resolve_runtime
  printf 'Ignition:          %s\n' "$IGNITION_VERSION"
  printf 'Jython checker:    %s\n' "$JYTHON_VERSION"
  printf 'Instance:          %s\n' "$DEV_INSTANCE_ID"
  printf 'Gateway:           http://%s:%s\n' "$GATEWAY_BIND_ADDRESS" "$GATEWAY_HTTP_PORT"
  printf 'Compose project:   %s\n' "$COMPOSE_PROJECT_NAME"
  printf 'Module cache:      %s\n' "$(module_cache_path)"
  case "$IGNITION_VERSION" in
    8.3.[0-7]) [[ "$JYTHON_VERSION" == 2.7.3 ]] || warn "Ignition $IGNITION_VERSION predates the 8.3.8 Jython 2.7.4 update; verify the embedded Jython version." ;;
    8.3.8|8.3.9) [[ "$JYTHON_VERSION" == 2.7.4 ]] || warn "Ignition $IGNITION_VERSION uses Jython 2.7.4; local checker is $JYTHON_VERSION." ;;
  esac
}

cmd_self_test() {
  bash -n "$ROOT_DIR/devctl" "$ROOT_DIR/scripts/lib.sh" "$ROOT_DIR/scripts/commands.sh" "$ROOT_DIR/hooks/check.sh" "$ROOT_DIR/hooks/test.sh" "$ROOT_DIR/hooks/gateway-smoke.sh"
  log 'Bash syntax OK'
  if have docker && docker compose version >/dev/null 2>&1; then
    stage_modules; stage_backup_from_config
    compose config --quiet
    log 'Docker Compose render OK'
  else
    warn 'Docker Compose unavailable; skipped Compose render check'
  fi
  log 'Foundation self-test passed'
}

jython_check_file() {
  local jar="$1" file="$2"
  java -jar "$jar" -c 'import sys; compile(open(sys.argv[1], "rb").read(), sys.argv[1], "exec")' "$file" >/dev/null
}

cmd_jython_check() {
  (($# > 0)) || die 'Usage: ./devctl jython-check <file|dir> [...]'
  ensure_jython
  local jar input file count=0
  jar="$(jython_jar)"
  for input in "$@"; do
    [[ -e "$input" ]] || die "Jython path does not exist: $input"
    if [[ -f "$input" ]]; then jython_check_file "$jar" "$input"; count=$((count+1))
    else
      while IFS= read -r -d '' file; do jython_check_file "$jar" "$file"; count=$((count+1)); done < <(find "$input" -type f -name '*.py' -print0)
    fi
  done
  log "Jython $JYTHON_VERSION parsed $count file(s) successfully"
}

cmd_check() {
  load_config
  "$ROOT_DIR/hooks/check.sh"
  run_project_cmd PROJECT_CHECK_CMD "$PROJECT_CHECK_CMD"
  if [[ -n "$JYTHON_SOURCE_PATHS" ]]; then
    local oldifs="$IFS"; IFS=':' read -r -a paths <<< "$JYTHON_SOURCE_PATHS"; IFS="$oldifs"
    cmd_jython_check "${paths[@]}"
  fi
  log 'Check phase passed'
}
cmd_test() { load_config; "$ROOT_DIR/hooks/test.sh"; run_project_cmd PROJECT_TEST_CMD "$PROJECT_TEST_CMD"; log 'Test phase passed'; }
cmd_build() { load_config; run_project_cmd PROJECT_BUILD_CMD "$PROJECT_BUILD_CMD"; stage_modules; log 'Build phase passed'; }
cmd_verify() {
  cmd_self_test; cmd_check; cmd_test; cmd_build; load_config
  if [[ "$VERIFY_GATEWAY" == 1 ]]; then SKIP_PROJECT_BUILD=1 gateway_up; gateway_wait 240; gateway_smoke; fi
  log 'Verification passed'
}

cmd_ci_local() {
  load_config; need act
  local args=(pull_request -j foundation)
  [[ "$ACT_OFFLINE" == 1 ]] && args+=(--pull=false --action-offline-mode)
  (cd "$ROOT_DIR" && act "${args[@]}")
}

cmd_module() {
  local action="${1:-}"; shift || true
  case "$action" in
    add) (($# == 1)) || die 'Usage: ./devctl module add <file.modl>'; [[ -f "$1" && "$1" == *.modl ]] || die "Expected existing .modl: $1"; mkdir -p "$PRIVATE_MODULE_DIR"; cp -f "$1" "$PRIVATE_MODULE_DIR/"; stage_modules; log "Added $(basename "$1")" ;;
    list) load_config; printf 'Global (%s):\n' "$(module_cache_path)"; find "$(module_cache_path)" -maxdepth 1 -type f -name '*.modl' -printf '  %f\n' 2>/dev/null || true; printf 'Checkout-local (%s):\n' "$PRIVATE_MODULE_DIR"; find "$PRIVATE_MODULE_DIR" -maxdepth 1 -type f -name '*.modl' -printf '  %f\n' 2>/dev/null || true ;;
    cache-path) module_cache_path ;;
    clear) mkdir -p "$PRIVATE_MODULE_DIR"; find "$PRIVATE_MODULE_DIR" -maxdepth 1 -type f -name '*.modl' -delete; stage_modules; log 'Cleared checkout-local modules' ;;
    *) die "Unknown module action: ${action:-<none>}" ;;
  esac
}

cmd_baseline() {
  local action="${1:-}"; shift || true; mkdir -p "$RESTORE_DIR"
  case "$action" in
    set) (($# == 1)) || die 'Usage: ./devctl baseline set <file.gwbk>'; [[ -f "$1" && "$1" == *.gwbk ]] || die "Expected existing .gwbk: $1"; cp -f "$1" "$RESTORE_DIR/restore.gwbk"; log "Staged baseline: $1" ;;
    clear) rm -f "$RESTORE_DIR/restore.gwbk"; log 'Cleared staged baseline' ;;
    status) load_config; printf 'Configured GATEWAY_BACKUP: %s\n' "${GATEWAY_BACKUP:-<none>}"; [[ -f "$RESTORE_DIR/restore.gwbk" ]] && printf 'Staged runtime backup: %s\n' "$RESTORE_DIR/restore.gwbk" || printf 'Staged runtime backup: <none>\n' ;;
    *) die "Unknown baseline action: ${action:-<none>}" ;;
  esac
}

require_eula() { load_config; [[ "$ACCEPT_IGNITION_EULA" == Y ]] || die "Ignition EULA not accepted. Review it, then run './devctl bootstrap --accept-eula'."; }

gateway_up() {
  require_eula; need docker; docker compose version >/dev/null 2>&1 || die 'Docker Compose v2 is required'
  load_config
  if [[ "${SKIP_PROJECT_BUILD:-0}" == 1 ]]; then stage_modules
  elif [[ -n "$PROJECT_BUILD_CMD" || -n "$PROJECT_MODULE_GLOB" ]]; then cmd_build
  else stage_modules; fi
  stage_backup_from_config
  log "Starting Ignition $IGNITION_VERSION at $(gateway_url)"
  compose up -d --build gateway
}

gateway_wait() {
  local timeout="${1:-180}"; need curl
  local url start now; url="$(gateway_url)"; start="$(date +%s)"
  log "Waiting up to ${timeout}s for $url"
  while ! curl --fail --silent --show-error --location --output /dev/null "$url/" 2>/dev/null; do
    now="$(date +%s)"; if ((now-start >= timeout)); then warn 'Gateway did not become ready'; compose logs --tail 150 gateway >&2 || true; return 1; fi
    sleep 3
  done
  log "Gateway is responding: $url"
}

gateway_smoke() { local url; url="$(gateway_url)"; GATEWAY_URL="$url" "$ROOT_DIR/hooks/gateway-smoke.sh"; }

cmd_gateway() {
  local action="${1:-}"; shift || true
  case "$action" in
    up) gateway_up ;;
    down) if [[ "${1:-}" == --volumes ]]; then compose down --volumes --remove-orphans; else compose down --remove-orphans; fi ;;
    reset) compose down --volumes --remove-orphans || true; gateway_up; gateway_wait 240 ;;
    restart) compose restart gateway ;;
    wait) local timeout=180; [[ "${1:-}" == --timeout ]] && { [[ -n "${2:-}" ]] || die '--timeout requires seconds'; timeout="$2"; }; gateway_wait "$timeout" ;;
    smoke) gateway_wait 180; gateway_smoke ;;
    status) compose ps; printf 'Gateway URL: %s\n' "$(gateway_url)" ;;
    logs) compose logs "$@" gateway ;;
    url) gateway_url ;;
    *) die "Unknown gateway action: ${action:-<none>}" ;;
  esac
}
