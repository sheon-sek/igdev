#!/usr/bin/env bash
# Module metadata, listing and pre-Gateway capability checks.

builtin_catalog_file(){ printf '%s/config/builtin-modules.tsv\n' "$ROOT_DIR"; }
capability_catalog_file(){ printf '%s/config/capability-modules.tsv\n' "$ROOT_DIR"; }

trim(){ local v="$1"; v="${v#"${v%%[![:space:]]*}"}"; v="${v%"${v##*[![:space:]]}"}"; printf '%s' "$v"; }
csv_has(){
	local csv="$1" needle="$2" x old="$IFS"
	IFS=',' read -r -a xs <<< "$csv"; IFS="$old"
	for x in "${xs[@]}"; do [[ "$(trim "$x")" == "$needle" ]] && return 0; done
	return 1
}
builtin_record(){ awk -F '\t' -v id="$1" '$1==id{print;exit}' "$(builtin_catalog_file)"; }
is_builtin(){ [[ -n "$(builtin_record "$1")" || "$1" == com.inductiveautomation.suite.* ]]; }

print_builtin_catalog(){
	printf '%-62s %s\n' 'MODULE ID' 'MODULE FILE'
	awk -F '\t' '!/^#/&&NF>=2{printf "%-62s %s\n",$1,$2}' "$(builtin_catalog_file)"
}
print_enabled_builtin(){
	load_config
	local id rec old="$IFS"
	if [[ -z "$GATEWAY_MODULES_ENABLED" ]]; then
		awk -F '\t' '!/^#/&&NF>=2{printf "  %-62s %s\n",$1,$2}' "$(builtin_catalog_file)"
		return
	fi
	IFS=',' read -r -a ids <<< "$GATEWAY_MODULES_ENABLED"; IFS="$old"
	for id in "${ids[@]}"; do
		id="$(trim "$id")"; rec="$(builtin_record "$id")"
		if [[ -n "$rec" ]]; then
			printf '  %-62s %s\n' "$id" "${rec#*$'\t'}"
		elif [[ "$id" == com.inductiveautomation.suite.* ]]; then
			printf '  %-62s %s\n' "$id" '<solution-suite-selector>'
		fi
	done
	return 0
}

xml_tag(){ printf '%s' "$1" | sed -n "s:.*<$2>[[:space:]]*\\([^<]*\\)[[:space:]]*</$2>.*:\\1:p" | head -n1; }
modl_info(){
	local f="$1" t xml id name ver
	[[ -f "$f" ]] || return 1; have jar || return 2
	[[ "$f" == /* ]] || f="$(cd "$(dirname "$f")"&&pwd)/$(basename "$f")"
	t="$(mktemp -d)"
	(cd "$t" && jar xf "$f" module.xml >/dev/null 2>&1) || { rm -rf "$t"; return 1; }
	[[ -s "$t/module.xml" ]] || { rm -rf "$t"; return 1; }
	xml="$(tr '\r\n' '  ' < "$t/module.xml")"; rm -rf "$t"
	id="$(xml_tag "$xml" id)"; name="$(xml_tag "$xml" name)"; ver="$(xml_tag "$xml" version)"
	[[ -n "$id" ]] || return 1
	printf '%s\t%s\t%s\n' "$id" "${name:-<unknown>}" "${ver:-<unknown>}"
}
private_records(){
	load_config
	local t="$(mktemp)" d f info id name ver
	d="$(module_cache_path)"; shopt -s nullglob
	for f in "$d"/*.modl "$PRIVATE_MODULE_DIR"/*.modl; do
		if info="$(modl_info "$f")"; then
			IFS=$'\t' read -r id name ver <<< "$info"
			[[ "$f" == "$PRIVATE_MODULE_DIR"/* ]] && src=local || src=global-cache
			printf '%s\t%s\t%s\t%s\t%s\n' "$id" "$name" "$ver" "$(basename "$f")" "$src" >> "$t"
		else
			printf 'file:%s\t<unreadable>\t-\t%s\tunknown\n' "$f" "$(basename "$f")" >> "$t"
		fi
	done
	shopt -u nullglob
	# dedupe by module id; checkout-local comes last and wins.
	awk -F '\t' '{if(!seen[$1]++)order[++n]=$1; row[$1]=$0}END{for(i=1;i<=n;i++)print row[order[i]]}' "$t"
	rm -f "$t"
}
private_has(){ local id rest; while IFS=$'\t' read -r id rest; do [[ "$id" == "$1" ]]&&return 0; done < <(private_records); return 1; }
enabled(){ load_config; [[ -z "$GATEWAY_MODULES_ENABLED" ]] || csv_has "$GATEWAY_MODULES_ENABLED" "$1"; }

print_private(){
	load_config
	local rows id name ver file src status cfg rec old="$IFS"
	rows="$(private_records)"
	printf '%-48s %-22s %-12s %-22s %-16s %s\n' 'MODULE ID' 'NAME' 'VERSION' 'ARTIFACT' 'SOURCE' 'STATUS'
	while IFS=$'\t' read -r id name ver file src; do
		[[ -n "$id" ]] || continue
		if [[ "$id" == file:* ]]; then status=UNREADABLE
		elif enabled "$id"; then status=enabled
		else status=staged-not-enabled; fi
		[[ "$id" == file:* ]] && id='<unknown>'
		printf '%-48s %-22s %-12s %-22s %-16s %s\n' "$id" "$name" "$ver" "$file" "$src" "$status"
	done <<< "$rows"
	if [[ -n "$GATEWAY_MODULES_ENABLED" ]]; then
		IFS=',' read -r -a ids <<< "$GATEWAY_MODULES_ENABLED"; IFS="$old"
		for cfg in "${ids[@]}"; do
			cfg="$(trim "$cfg")"; is_builtin "$cfg"&&continue
			private_has "$cfg" || printf '%-48s %-22s %-12s %-22s %-16s %s\n' "$cfg" '<unknown>' '-' '<missing>' '-' 'MISSING-ARTIFACT'
		done
	fi
}

rest_module(){
	local p="${1#* }" seg old="$IFS"
	[[ "$p" == *"/data/api/v1/resources/"* ]] || return 1
	p="${p#*/data/api/v1/resources/}"; p="${p%%\?*}"
	IFS='/' read -r -a segs <<< "$p"; IFS="$old"
	for seg in "${segs[@]}"; do [[ "$seg" == com.* ]]&&{ printf '%s\n' "$seg"; return; }; done
	return 1
}
cap_modules(){
	local c="$(trim "$1")" kind pat mods desc r
	[[ "$c" == module:* ]]&&{ printf '%s\n' "${c#module:}"; return; }
	[[ "$c" == com.* ]]&&{ printf '%s\n' "$c"; return; }
	r="$(rest_module "$c" 2>/dev/null)"&&{ printf '%s\n' "$r"; return; }
	while IFS=$'\t' read -r kind pat mods desc; do
		[[ -n "$kind" && "$kind" != \#* ]]||continue
		[[ "$kind" == exact && "$c" == "$pat" ]]&&{ printf '%s\n' "$mods"; return; }
		[[ "$kind" == prefix && "$c" == "$pat"* ]]&&{ printf '%s\n' "$mods"; return; }
	done < "$(capability_catalog_file)"
	return 1
}
module_ready(){
	local id="$1"
	enabled "$id" || { printf 'not enabled by GATEWAY_MODULES_ENABLED'; return 1; }
	is_builtin "$id"&&return 0
	private_has "$id"||{ printf 'no matching .modl artifact found'; return 1; }
}
require_one(){
	local c="$1" mods id why fail=0 old="$IFS"
	mods="$(cap_modules "$c")"||{ echo "[module-preflight] UNKNOWN: no mapping for $c" >&2; return 2; }
	IFS=',' read -r -a ids <<< "$mods"; IFS="$old"
	for id in "${ids[@]}"; do
		id="$(trim "$id")"
		if why="$(module_ready "$id")"; then echo "[module-preflight] OK: $c -> $id"
		else echo "[module-preflight] ERROR: $c requires $id ($why)" >&2; fail=1; fi
	done
	if ((fail)); then
		echo "[module-preflight] Fix: ./devctl module enable ${mods//,/ }" >&2
		for id in "${ids[@]}"; do id="$(trim "$id")"; is_builtin "$id"||private_has "$id"||echo "[module-preflight] Add artifact: ./devctl module add /path/to/module.modl" >&2; done
		return 1
	fi
}
enable_ids(){
	(($#))||die 'Usage: ./devctl module enable <module-id> [...]'
	[[ -f "$ROOT_DIR/.env" ]]||cp "$ROOT_DIR/.env.example" "$ROOT_DIR/.env"
	load_config; local cur="$GATEWAY_MODULES_ENABLED" id changed=0
	[[ -z "$cur" ]]&&{ log 'Module whitelist is empty; modules are already unrestricted.'; return; }
	for id in "$@"; do csv_has "$cur" "$id"&&continue; cur="$cur,$id"; changed=1; done
	((changed))||{ log 'Requested modules are already enabled.'; return; }
	sed -i.bak "s#^GATEWAY_MODULES_ENABLED=.*#GATEWAY_MODULES_ENABLED=$cur#" "$ROOT_DIR/.env"; rm -f "$ROOT_DIR/.env.bak"
	log 'Updated .env; use ./devctl gateway reset before runtime verification.'
}
validate_modules(){
	load_config; local fail=0 id old="$IFS"
	if [[ -n "$GATEWAY_MODULES_ENABLED" ]]; then
		IFS=',' read -r -a ids <<< "$GATEWAY_MODULES_ENABLED"; IFS="$old"
		for id in "${ids[@]}"; do
			id="$(trim "$id")"
			if ! is_builtin "$id" && ! private_has "$id"; then echo "[module-preflight] ERROR: $id is enabled but its .modl is missing." >&2; fail=1; fi
			if [[ "$id" == com.inductiveautomation.opcua.drivers.* ]]&&!csv_has "$GATEWAY_MODULES_ENABLED" com.inductiveautomation.opcua; then
				echo "[module-preflight] ERROR: $id requires com.inductiveautomation.opcua." >&2; fail=1
			fi
		done
	fi
	((fail==0))&&log 'Module configuration preflight passed'
	return "$fail"
}
scan_file_capabilities(){
	local f="$1" out="$2"
	grep -Eo 'system\.[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*' "$f" 2>/dev/null >>"$out"||true
	grep -Eo '/data/api/v1/resources(/[A-Za-z0-9._~%:-]+)+' "$f" 2>/dev/null >>"$out"||true
}
scan_paths(){
	(($#))||die 'Usage: ./devctl module scan <file|dir> [...]'
	local t="$(mktemp)" p f c mods fail=0 count=0
	for p in "$@"; do
		[[ -e "$p" ]]||{ warn "Module scan path missing: $p"; continue; }
		if [[ -f "$p" ]]; then
			scan_file_capabilities "$p" "$t"
		else
			while IFS= read -r -d '' f; do
				scan_file_capabilities "$f" "$t"
			done < <(find "$p" -type f \( -name '*.py' -o -name '*.json' -o -name '*.js' -o -name '*.ts' -o -name '*.tsx' -o -name '*.java' -o -name '*.kt' -o -name '*.sh' \) -print0)
		fi
	done
	sort -u "$t" -o "$t"
	while IFS= read -r c; do
		mods="$(cap_modules "$c" 2>/dev/null)"||continue
		count=$((count+1)); require_one "$c"||fail=1
	done < "$t"
	rm -f "$t"
	((count))&&log "Checked $count module-dependent capability reference(s)"
	return "$fail"
}

cmd_module(){
	local a="${1:-}"; shift||true
	case "$a" in
	add)
		(($#==1))||die 'Usage: ./devctl module add <file.modl>'; need jar
		local info id name ver; info="$(modl_info "$1")"||die "Could not read module.xml from $1"
		IFS=$'\t' read -r id name ver <<< "$info"; mkdir -p "$PRIVATE_MODULE_DIR"; cp -f "$1" "$PRIVATE_MODULE_DIR/"; stage_modules
		log "Added $name $ver ($id) from $(basename "$1")" ;;
	list)
		[[ $# -le 1 ]]||die 'Usage: ./devctl module list [--private|--built-in]'
		case "${1:-}" in
		'') echo 'Built-in modules effective for this environment:'; print_enabled_builtin; echo; echo 'Private / third-party modules:'; print_private ;;
		--built-in) echo 'Built-in modules effective for this environment:'; print_enabled_builtin ;;
		--private) echo 'Private / third-party modules:'; print_private ;;
		*) die "Unknown module list option: $1" ;; esac ;;
	catalog) print_builtin_catalog ;;
	require) (($#))||die 'Usage: ./devctl module require <capability> [...]'; local c rc=0; for c in "$@"; do require_one "$c"||rc=1; done; return "$rc" ;;
	scan) scan_paths "$@" ;;
	validate) validate_modules ;;
	enable) enable_ids "$@" ;;
	cache-path) module_cache_path ;;
	clear) mkdir -p "$PRIVATE_MODULE_DIR"; find "$PRIVATE_MODULE_DIR" -maxdepth 1 -type f -name '*.modl' -delete; stage_modules; log 'Cleared checkout-local modules' ;;
	*) die "Unknown module action: ${a:-<none>}" ;;
	esac
}
