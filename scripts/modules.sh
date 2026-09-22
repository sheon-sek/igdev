#!/usr/bin/env bash
# Module metadata, listing and pre-Gateway capability checks.

builtin_catalog_file(){ printf '%s/config/builtin-modules.tsv\n' "$ROOT_DIR"; }
capability_catalog_file(){ printf '%s/config/capability-modules.tsv\n' "$ROOT_DIR"; }
native_function_catalog_file(){ printf '%s/config/native-system-functions.tsv\n' "$ROOT_DIR"; }
rest_catalog_file(){
	[[ -n "${IGNITION_VERSION:-}" ]] || load_config
	local f="${REST_ENDPOINT_CATALOG:-}"
	if [[ -n "$f" ]]; then
		[[ "$f" == /* ]] || f="$ROOT_DIR/$f"
		printf '%s\n' "$f"
	else
		printf '%s/config/rest-endpoints-%s.tsv\n' "$ROOT_DIR" "$IGNITION_VERSION"
	fi
}

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

native_function_record(){
	awk -F '\t' -v fn="$1" '!/^#/ && $1==fn{print;exit}' "$(native_function_catalog_file)"
}
native_function_known(){ [[ -n "$(native_function_record "$1")" ]]; }
native_function_class(){
	local rec; rec="$(native_function_record "$1")"; [[ -n "$rec" ]] || return 1
	printf '%s\n' "$(printf '%s\n' "$rec" | cut -f2)"
}
native_function_note(){
	local rec; rec="$(native_function_record "$1")"; [[ -n "$rec" ]] || return 1
	printf '%s\n' "$(printf '%s\n' "$rec" | cut -f4-)"
}

rest_request_parts(){
	local c method='' path
	c="$(trim "$1")"
	if [[ "$c" =~ ^([A-Za-z]+)[[:space:]]+(/data/[^[:space:]]+)$ ]]; then
		method="${BASH_REMATCH[1]^^}"
		case "$method" in GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|TRACE) ;; *) return 1 ;; esac
		path="${BASH_REMATCH[2]}"
	elif [[ "$c" == /data/* ]]; then
		path="$c"
	else
		return 1
	fi
	path="${path%%\?*}"
	[[ -n "$path" ]] || return 1
	printf '%s\t%s\n' "${method:-*}" "$path"
}
rest_catalog_lookup(){
	local parsed method path catalog
	parsed="$(rest_request_parts "$1")" || return 1
	IFS=$'\t' read -r method path <<< "$parsed"
	catalog="$(rest_catalog_file)"
	[[ -s "$catalog" ]] || { printf '[module-preflight] REST catalog unavailable for Ignition %s: %s\n' "$IGNITION_VERSION" "$catalog" >&2; return 3; }
	awk -F '\t' -v method="$method" -v path="$path" '
		function pathmatch(t,p, ta,pa,tn,pn,i) {
			tn=split(t,ta,"/"); pn=split(p,pa,"/"); if (tn != pn) return 0
			for (i=1; i<=tn; i++) { if (ta[i] ~ /^\{[^}]+\}$/) continue; if (ta[i] != pa[i]) return 0 }
			return 1
		}
		!/^#/ && NF>=4 { if (method != "*" && $1 != method) next; if (pathmatch($2,path)) print $3 "\t" $4 "\t" $1 "\t" $2 }
	' "$catalog" | sort -u
}
rest_requirement(){
	local matches requirements count
	matches="$(rest_catalog_lookup "$1")" || return $?
	[[ -n "$matches" ]] || return 1
	requirements="$(printf '%s\n' "$matches" | awk -F '\t' '!seen[$1 FS $2]++ {print $1 "\t" $2 "\t" $4}')"
	count="$(printf '%s\n' "$requirements" | awk 'NF{n++} END{print n+0}')"
	if (( count != 1 )); then printf '[module-preflight] ERROR: ambiguous REST ownership for %s\n' "$1" >&2; return 2; fi
	printf '%s\n' "$requirements"
}
cap_modules(){
	local c="$(trim "$1")" kind pat mods desc r rec fn cls note rest_kind rest_template
	[[ "$c" == module:* ]]&&{ printf '%s\n' "${c#module:}"; return; }
	[[ "$c" == com.* ]]&&{ printf '%s\n' "$c"; return; }
	if rest_request_parts "$c" >/dev/null 2>&1; then
		r="$(rest_requirement "$c")" || return $?
		IFS=$'\t' read -r rest_kind mods rest_template <<< "$r"
		[[ "$rest_kind" == platform ]] && printf '%s\n' '@platform' || printf '%s\n' "$mods"
		return 0
	fi
	if [[ "$c" == system.* ]]; then
		rec="$(native_function_record "$c")"; [[ -n "$rec" ]] || return 1
		IFS=$'\t' read -r fn cls mods note <<< "$rec"
		[[ "$mods" == "-" ]] && return 0
		printf '%s\n' "$mods"; return 0
	fi
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
	local c="$1" mods id why fail=0 old="$IFS" cls note
	if ! mods="$(cap_modules "$c")"; then
		if [[ "$c" == system.* ]]; then
			echo "[module-preflight] ERROR: unknown or non-Gateway Ignition 8.3 native function: $c" >&2
			echo "[module-preflight] Check the function name, Gateway scope, and target Ignition version." >&2
		elif rest_request_parts "$c" >/dev/null 2>&1; then
			echo "[module-preflight] ERROR: REST operation is not present in the Ignition $IGNITION_VERSION catalog: $c" >&2
		else echo "[module-preflight] UNKNOWN: no mapping for $c" >&2; fi
		return 2
	fi
	if [[ "$c" == system.* && -z "$mods" ]]; then cls="$(native_function_class "$c")"; echo "[module-preflight] OK: $c -> $cls (no optional module required)"; return 0; fi
	if [[ "$mods" == '@platform' ]]; then echo "[module-preflight] OK: $c -> platform"; return 0; fi
	IFS=',' read -r -a ids <<< "$mods"; IFS="$old"
	for id in "${ids[@]}"; do
		id="$(trim "$id")"
		if why="$(module_ready "$id")"; then echo "[module-preflight] OK: $c -> $id"; else echo "[module-preflight] ERROR: $c requires $id ($why)" >&2; fail=1; fi
	done
	if ((fail)); then
		echo "[module-preflight] Fix: ./devctl module enable ${mods//,/ }" >&2
		for id in "${ids[@]}"; do id="$(trim "$id")"; is_builtin "$id"||private_has "$id"||echo "[module-preflight] Add artifact: ./devctl module add /path/to/module.modl" >&2; done
		return 1
	fi
	if [[ "$c" == system.* && "$(native_function_class "$c")" == conditional ]]; then note="$(native_function_note "$c")"; [[ -z "$note" ]] || echo "[module-preflight] NOTE: $c: $note"; fi
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
validate_rest_catalog(){
	load_config
	local catalog count
	catalog="$(rest_catalog_file)"
	if [[ ! -s "$catalog" ]]; then warn "No REST endpoint catalog for Ignition $IGNITION_VERSION: $catalog. REST references will fail closed when scanned."; return 0; fi
	if ! awk -F '\t' '
		/^#/ {next}
		NF != 4 {bad=1; next}
		$1 !~ /^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|TRACE)$/ {bad=1}
		$3 !~ /^(platform|module|private-module)$/ {bad=1}
		$3 == "platform" && $4 != "-" {bad=1}
		$3 != "platform" && ($4 == "" || $4 == "-") {bad=1}
		{ key=$1 FS $2; if (seen[key]++) bad=1 }
		END {exit bad}
	' "$catalog"; then echo "[module-preflight] ERROR: REST endpoint catalog is malformed or contains duplicate METHOD/path entries: $catalog" >&2; return 1; fi
	count="$(awk -F '\t' '!/^#/ && NF>=4{n++} END{print n+0}' "$catalog")"
	log "REST endpoint catalog OK ($count operations for Ignition $IGNITION_VERSION)"
}
validate_modules(){
	load_config; local fail=0 id old="$IFS"
	if [[ -n "$GATEWAY_MODULES_ENABLED" ]]; then
		IFS=',' read -r -a ids <<< "$GATEWAY_MODULES_ENABLED"; IFS="$old"
		for id in "${ids[@]}"; do
			id="$(trim "$id")"
			if ! is_builtin "$id" && ! private_has "$id"; then echo "[module-preflight] ERROR: $id is enabled but its .modl is missing." >&2; fail=1; fi
			if [[ "$id" == com.inductiveautomation.opcua.drivers.* ]]&&!csv_has "$GATEWAY_MODULES_ENABLED" com.inductiveautomation.opcua; then echo "[module-preflight] ERROR: $id requires com.inductiveautomation.opcua." >&2; fail=1; fi
		done
	fi
	validate_rest_catalog || fail=1
	((fail==0))&&log 'Module configuration preflight passed'
	return "$fail"
}
scan_file_capabilities(){
	local f="$1" out="$2" path_tmp method_tmp
	local rest_path_re='/data/[A-Za-z0-9._~%:{}@+,=-]+(/[A-Za-z0-9._~%:{}@+,=-]+)*/?'
	grep -Eo 'system\.[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+' "$f" 2>/dev/null >>"$out"||true
	path_tmp="$(mktemp)"; method_tmp="$(mktemp)"
	grep -Eo "$rest_path_re" "$f" 2>/dev/null | sort -u >"$path_tmp" || true
	grep -Eio "(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|TRACE)[[:space:]]+$rest_path_re" "$f" 2>/dev/null | awk '{m=toupper($1); sub(/^[^[:space:]]+[[:space:]]+/, ""); print m " " $0}' | sort -u >"$method_tmp" || true
	awk 'NR==FNR {line=$0; path=line; sub(/^[A-Z]+[[:space:]]+/, "", path); known[path]=1; print line; next} !known[$0] {print}' "$method_tmp" "$path_tmp" >>"$out"
	rm -f "$path_tmp" "$method_tmp"
}
scan_paths(){
	(($#))||die 'Usage: ./devctl module scan <file|dir> [...]'
	local t="$(mktemp)" p f c fail=0 count=0
	for p in "$@"; do
		[[ -e "$p" ]]||{ warn "Module scan path missing: $p"; continue; }
		if [[ -f "$p" ]]; then scan_file_capabilities "$p" "$t"; else
			while IFS= read -r -d '' f; do scan_file_capabilities "$f" "$t"; done < <(find "$p" -type f \( -name '*.py' -o -name '*.json' -o -name '*.js' -o -name '*.ts' -o -name '*.tsx' -o -name '*.java' -o -name '*.kt' -o -name '*.sh' \) -print0)
		fi
	done
	sort -u "$t" -o "$t"
	while IFS= read -r c; do
		if [[ "$c" == system.* ]] || rest_request_parts "$c" >/dev/null 2>&1; then count=$((count+1)); require_one "$c"||fail=1; continue; fi
		if cap_modules "$c" >/dev/null 2>&1; then count=$((count+1)); require_one "$c"||fail=1; fi
	done < "$t"
	rm -f "$t"
	((count))&&log "Checked $count native/REST/module capability reference(s)"
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
