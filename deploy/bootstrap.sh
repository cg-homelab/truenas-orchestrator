#!/usr/bin/env bash
# bootstrap.sh: first-run setup of a TrueNAS server for truenas-orchestrator (D11, D32, D33).
#
# Run as root on the TrueNAS host (`sudo -i`, or over SSH), from a checkout of this repository's deploy/
# directory, with bootstrap.env beside it (copy bootstrap.env.example). Supported: TrueNAS 25.10 through
# 27.x (D36). docs/hand-install.md is the same procedure by hand; both reach the end state that the
# orchestrator's environment-readiness check accepts.
#
# Usage: bootstrap.sh [--env FILE] [--dry-run] [--yes] [--create-api-key] [--help]
#
# Phases:
#   Check    read-only; prints every item as ok / will create / will change / blocked
#   Confirm  "Apply N changes? [y/N]", skipped by --yes; --dry-run stops after Check
#   Apply    the steps in order, B0 to B13; each re-reads the state at the point of write
# A second run on a configured server prints "nothing to do" and exits 0.
#
# Every TrueNAS object is created through `midclt call` (`-j` for job methods).
#
# Conventions that deploy/deploy_test.go parses (keep them exact):
#
# - Step lines. Each step starts with a comment line at column 0 of the form `# step:B<n> <title>`, for
#   n = 0..13 in ascending order, and no other line starts with `# step:`. docs/hand-install.md has the
#   same ids and titles, in the same order, as `## B<n> <title>` headings.
# - Payload heredocs. The parameters of every TrueNAS write come from a payload function. Inside it, a
#   line holding only `# payload:<name>` (leading whitespace allowed) is followed by a line holding only
#   `cat <<EOF` (leading whitespace allowed). Every following line up to the first line that is exactly
#   `EOF` (column 0) is the body. The body is one JSON array, the method's whole params list, once
#   placeholders are substituted. The names are group, user, dataset-parent, dataset-root,
#   dataset-database, dataset-state, perm, perm-state, networks-app, api-key and orchestrator-app.
# - Placeholders. In a body, `${VAR}` (upper-case letters, digits and `_`) is replaced by the variable's
#   value verbatim, with no quoting added. A variable whose name ends in `_JSON` holds an already
#   JSON-encoded string, quotes included. Every other value is validated in B0 to contain no character
#   that needs JSON escaping, so substituting it inside a JSON string literal is safe.
#
# Secrets: nothing secret is logged, and `set -x` is never enabled. The only secret this script ever
# holds is the API key that B11 creates with --create-api-key; it is printed once and never written to
# disk. The UI certificate's private key never leaves the jq filter that extracts the public PEM.

set +x # never trace: a trace could print the API key (SHELLOPTS can turn xtrace on)
set -euo pipefail
shopt -s inherit_errexit
umask 027

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
readonly SCRIPT_DIR
readonly NETWORKS_APP="homelab-networks"
readonly HOMELAB_NETWORKS=(home proxy isolated databases)
readonly API_KEY_NAME="truenas-orchestrator"
readonly HOMELAB_NAME="homelab"

ENV_FILE="$SCRIPT_DIR/bootstrap.env"
DRY_RUN=0
ASSUME_YES=0
CREATE_API_KEY=0

CHANGES=0
BLOCKED=0
PLAN=()
PLAN_HAS_B8=""
TMP_DIR=""

usage() {
	cat <<'USAGE'
Usage: bootstrap.sh [--env FILE] [--dry-run] [--yes] [--create-api-key] [--help]

Prepares this TrueNAS server for truenas-orchestrator: the homelab group and user, the storage and state
datasets and their permissions, the homelab-networks app, the deploy key, the homelab repo clone,
compose/.global.env, optionally an API key, the TrueNAS CA file and the orchestrator app.

  --env FILE        read inputs from FILE (default: bootstrap.env next to this script;
                    see bootstrap.env.example)
  --dry-run         run the read-only Check phase and stop
  --yes             apply without asking "Apply N changes? [y/N]"
  --create-api-key  create the orchestrator's TrueNAS API key if absent and print it once
  --help            show this help

Run as root on the TrueNAS host. See docs/hand-install.md for the same steps by hand.
USAGE
}

die() {
	printf 'bootstrap: %s\n' "$*" >&2
	exit 1
}

# ---------------------------------------------------------------------------------------------------------
# Output and planning helpers.

# item STATUS STEP MESSAGE: one line of the Check report. "will create" and "will change" count as changes,
# "blocked" stops the run before Apply.
item() {
	local status=$1 step=$2
	shift 2
	printf '  %-14s %-4s %s\n' "[$status]" "$step" "$*"
	case $status in
	"will create" | "will change") CHANGES=$((CHANGES + 1)) ;;
	blocked) BLOCKED=$((BLOCKED + 1)) ;;
	esac
}

# plan FUNCTION: queue an apply function once, in Check order.
plan() {
	local f
	for f in "${PLAN[@]+"${PLAN[@]}"}"; do
		[[ $f == "$1" ]] && return 0
	done
	PLAN+=("$1")
}

say() {
	printf '  %s\n' "$*"
}

# json_str VALUE: VALUE as a JSON string literal.
json_str() {
	jq -cn --arg v "$1" '$v'
}

# ---------------------------------------------------------------------------------------------------------
# midclt helpers. The local middleware socket authenticates root without a key (D35).

# mid METHOD [JSON-ARG...]: a read call; prints the JSON result.
mid() {
	local method=$1 out
	shift
	if ! out=$(midclt call "$method" "$@"); then
		die "midclt call $method failed (middleware error above)"
	fi
	printf '%s\n' "$out"
}

# mid_payload [-j] METHOD PAYLOAD: a write call whose params are the JSON array PAYLOAD; each element is
# passed as one argument. With -j the method is a job and midclt waits for it. Prints the result.
mid_payload() {
	local job=() method payload out
	local -a args=()
	if [[ $1 == -j ]]; then
		job=(-j)
		shift
	fi
	method=$1 payload=$2
	jq -e 'type == "array"' >/dev/null 2>&1 <<<"$payload" || die "internal error: the $method payload is not a JSON array"
	mapfile -t args < <(jq -c '.[]' <<<"$payload")
	if ! out=$(midclt call "${job[@]+"${job[@]}"}" "$method" "${args[@]}"); then
		die "midclt call $method failed (middleware error above); nothing after this step was applied"
	fi
	printf '%s\n' "$out"
}

# ---------------------------------------------------------------------------------------------------------
# Inputs.

parse_args() {
	while (($#)); do
		case $1 in
		--env)
			(($# >= 2)) || die "--env needs a file"
			ENV_FILE=$2
			shift
			;;
		--env=*) ENV_FILE=${1#--env=} ;;
		--dry-run) DRY_RUN=1 ;;
		--yes | -y) ASSUME_YES=1 ;;
		--create-api-key) CREATE_API_KEY=1 ;;
		--help | -h)
			usage
			exit 0
			;;
		*) die "unknown argument: $1 (see --help)" ;;
		esac
		shift
	done
}

readonly ENV_KEYS=(HOMELAB_STORAGE APPCONFIG_STORAGE DATABASE_STORAGE MEDIA_STORAGE ORCH_STATE_STORAGE
	PUID PGID REPO_URL ORCH_IMAGE ORCH_APP_NAME TRUENAS_API_USER)

# load_env FILE: parse KEY=VALUE lines for the keys above. The file is never sourced.
load_env() {
	local file=$1 line key value k known
	[[ -f $file ]] || die "$file not found; copy bootstrap.env.example to bootstrap.env next to this script and edit it, or pass --env FILE"
	for k in "${ENV_KEYS[@]}"; do
		printf -v "$k" '%s' ""
	done
	while IFS= read -r line || [[ -n $line ]]; do
		line=${line%$'\r'}
		[[ $line =~ ^[[:space:]]*(#|$) ]] && continue
		[[ $line =~ ^([A-Z_][A-Z0-9_]*)=(.*)$ ]] || die "$file: not a KEY=VALUE line: $line"
		key=${BASH_REMATCH[1]}
		value=${BASH_REMATCH[2]}
		if [[ $value =~ ^\"(.*)\"$ || $value =~ ^\'(.*)\'$ ]]; then
			value=${BASH_REMATCH[1]}
		fi
		known=0
		for k in "${ENV_KEYS[@]}"; do
			[[ $k == "$key" ]] && known=1
		done
		((known)) || die "$file: unknown key $key"
		printf -v "$key" '%s' "$value"
	done <"$file"
	: "${ORCH_APP_NAME:=truenas-orchestrator}"
	: "${TRUENAS_API_USER:=truenas_admin}"
}

# ---------------------------------------------------------------------------------------------------------
# Dataset resolution (D32): a dataset's name comes from the deepest existing dataset whose mountpoint
# contains the path, never from string surgery on /mnt/.

DATASETS_JSON=""

refresh_datasets() {
	DATASETS_JSON=$(mid pool.dataset.query '[["type","=","FILESYSTEM"]]' \
		'{"extra":{"retrieve_children":false,"retrieve_user_props":false}}')
}

# dataset_at PATH: the dataset mounted exactly at PATH as compact JSON, or nothing.
dataset_at() {
	jq -c --arg p "$1" 'map(select(.mountpoint == $p)) | first // empty' <<<"$DATASETS_JSON"
}

# containing_dataset PATH: the deepest dataset whose mountpoint is a proper ancestor of PATH. The root
# filesystem ("/") and boot-pool datasets never count.
containing_dataset() {
	jq -c --arg p "$1" '
		map(select((.mountpoint | type) == "string" and (.mountpoint | startswith("/")) and .mountpoint != "/"))
		| map(select((.pool // (.name | split("/") | first)) != "boot-pool"))
		| map(select(.mountpoint as $m | $m != $p and ($p | startswith($m + "/"))))
		| sort_by(.mountpoint | length) | last // empty' <<<"$DATASETS_JSON"
}

# resolve_root PATH: sets RES_STATE to one of dataset | plain | create | blocked, plus
#   RES_DATASET  the dataset name (dataset, create)
#   RES_PARENTS  missing parent dataset names, shallowest first (create)
#   RES_REASON   why (plain, blocked)
resolve_root() {
	local path=$1 ds parent pname pmount rel prefix comp
	RES_STATE="" RES_DATASET="" RES_PARENTS=() RES_REASON=""
	ds=$(dataset_at "$path")
	if [[ -n $ds ]]; then
		RES_DATASET=$(jq -r '.name' <<<"$ds")
		if [[ $(jq -r '.locked // false' <<<"$ds") == true ]]; then
			RES_STATE=blocked RES_REASON="dataset $RES_DATASET is locked; unlock it in Datasets"
		else
			RES_STATE=dataset
		fi
		return 0
	fi
	if [[ -L $path ]]; then
		RES_STATE=blocked RES_REASON="$path is a symlink; a storage root must be a real directory"
		return 0
	fi
	if [[ -e $path && ! -d $path ]]; then
		RES_STATE=blocked RES_REASON="$path exists and is not a directory"
		return 0
	fi
	parent=$(containing_dataset "$path")
	[[ -n $parent ]] || {
		RES_STATE=blocked RES_REASON="no dataset contains $path; it must be under a pool's mountpoint"
		return 0
	}
	pname=$(jq -r '.name' <<<"$parent")
	pmount=$(jq -r '.mountpoint' <<<"$parent")
	if [[ $(jq -r '.locked // false' <<<"$parent") == true ]]; then
		RES_STATE=blocked RES_REASON="the containing dataset $pname is locked; unlock it in Datasets"
		return 0
	fi
	if [[ -d $path ]]; then
		RES_STATE=plain RES_REASON="$path is a plain directory in dataset $pname, not a dataset of its own; left as is"
		return 0
	fi
	rel=${path#"$pmount"}
	rel=${rel#/}
	prefix=$pmount
	RES_DATASET=$pname
	local -a comps
	IFS=/ read -r -a comps <<<"$rel"
	local i last=$((${#comps[@]} - 1))
	for i in "${!comps[@]}"; do
		comp=${comps[$i]}
		prefix=${prefix%/}/$comp
		RES_DATASET=$RES_DATASET/$comp
		if ((i < last)); then
			if [[ -e $prefix || -L $prefix ]]; then
				RES_STATE=blocked
				RES_REASON="$prefix exists as a plain directory inside dataset $pname, so no dataset can be created under it; create $path by hand or make $prefix a dataset"
				return 0
			fi
			RES_PARENTS+=("$RES_DATASET")
		fi
	done
	RES_STATE=create
}

# dataset_prop_differences PATH RECORDSIZE: advisory list of D32 properties an existing dataset differs on.
dataset_prop_differences() {
	jq -r --arg p "$1" --arg rs "$2" '
		(map(select(.mountpoint == $p)) | first // {}) as $d
		| [["acltype", "POSIX"], ["aclmode", "DISCARD"], ["atime", "OFF"], ["compression", "LZ4"], ["recordsize", $rs]]
		| map(. as [$k, $want] | ($d[$k].value // "" | tostring | ascii_upcase) as $got
			| select($got != "" and $got != $want) | "\($k)=\($got) (D32: \($want))")
		| join(", ")' <<<"$DATASETS_JSON"
}

# ---------------------------------------------------------------------------------------------------------
# step:B0 Preflight
# The TrueNAS version is in range, Docker is running, the inputs are well formed.

# A dataset or directory below a pool's root: /mnt/<pool>/<at least one more component>.
readonly PATH_RE='^/mnt/[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)+$'

check_b0() {
	local cmd ok=1 v major minor status k p f before
	for cmd in midclt jq git ssh-keygen install hostname stat mktemp openssl; do
		if ! command -v "$cmd" >/dev/null 2>&1; then
			item blocked B0 "required command $cmd not found"
			ok=0
		fi
	done
	((EUID == 0)) || {
		item blocked B0 "must run as root (sudo -i)"
		ok=0
	}
	((ok)) || return 0
	for f in "$SCRIPT_DIR/homelab-networks.yaml" "$SCRIPT_DIR/compose.yaml"; do
		[[ -f $f ]] || item blocked B0 "$f missing; run bootstrap.sh from a checkout of the deploy/ directory"
	done

	v=$(mid system.version)
	v=${v//\"/}
	v=${v#TrueNAS-}
	v=${v#SCALE-}
	if [[ $v =~ ^([0-9]+)\.([0-9]+) ]]; then
		major=${BASH_REMATCH[1]} minor=${BASH_REMATCH[2]}
		major=$((10#$major)) minor=$((10#$minor))
		if ((major < 25 || (major == 25 && minor < 10) || major >= 28)); then
			item blocked B0 "TrueNAS $v is not supported: this orchestrator supports TrueNAS 25.10 through 27.x"
		else
			item ok B0 "TrueNAS $v"
		fi
	else
		item blocked B0 "cannot parse the TrueNAS version \"$v\": this orchestrator supports TrueNAS 25.10 through 27.x"
	fi

	status=$(mid docker.status | jq -r '.status')
	if [[ $status == RUNNING ]]; then
		item ok B0 "Docker is running"
	else
		item blocked B0 "Docker is $status: choose an apps pool in Apps > Configuration > Choose Pool"
	fi

	before=$BLOCKED
	for k in "${ENV_KEYS[@]}"; do
		[[ -n ${!k} ]] || item blocked B0 "$ENV_FILE: $k is empty"
	done
	local -a roots=(HOMELAB_STORAGE APPCONFIG_STORAGE DATABASE_STORAGE MEDIA_STORAGE ORCH_STATE_STORAGE)
	for k in "${roots[@]}"; do
		p=${!k}
		[[ -n $p ]] || continue
		if [[ ! $p =~ $PATH_RE || /$p/ == */./* || /$p/ == */../* ]]; then
			item blocked B0 "$k=$p: must be an absolute path below /mnt/<pool>/ of letters, digits, '.', '_', '-', with no trailing slash"
		fi
	done
	local i j a b
	for i in "${!roots[@]}"; do
		for j in "${!roots[@]}"; do
			((i < j)) || continue
			a=${!roots[$i]} b=${!roots[$j]}
			[[ -n $a && -n $b ]] || continue
			if [[ $a == "$b" || $a == "$b"/* || $b == "$a"/* ]]; then
				item blocked B0 "${roots[$i]} and ${roots[$j]} must be distinct and not inside each other"
			fi
		done
	done
	for k in PUID PGID; do
		if [[ ! ${!k} =~ ^(0|[1-9][0-9]*)$ ]]; then
			item blocked B0 "$k=${!k}: must be a non-negative integer"
		elif ((${!k} == 0)); then
			item blocked B0 "$k=0 is root; the homelab identity must not be root (D32)"
		elif ((${!k} == 950)); then
			item blocked B0 "$k=950 is truenas_admin; the homelab identity must be its own account (D32)"
		elif [[ $k == PUID ]] && ((PUID != 1000)); then
			# M4: the published image creates only uid 1000, and ssh (git fetch) needs a passwd entry.
			item blocked B0 "PUID=$PUID: the orchestrator image runs as uid 1000 in M4; set PUID=1000 (any-uid images are a follow-up)"
		fi
	done
	[[ $ORCH_APP_NAME =~ ^[a-z]([-a-z0-9]*[a-z0-9])?$ ]] ||
		item blocked B0 "ORCH_APP_NAME=$ORCH_APP_NAME: lower-case letters, digits and '-' only"
	[[ $ORCH_APP_NAME != "$NETWORKS_APP" ]] ||
		item blocked B0 "ORCH_APP_NAME must not be $NETWORKS_APP (the orchestrator never touches its own app entry, D11)"
	[[ $TRUENAS_API_USER =~ ^[A-Za-z_][A-Za-z0-9_.-]*$ ]] ||
		item blocked B0 "TRUENAS_API_USER=$TRUENAS_API_USER: not a valid user name"
	[[ $ORCH_IMAGE =~ ^[A-Za-z0-9][A-Za-z0-9._/:@-]*$ ]] ||
		item blocked B0 "ORCH_IMAGE=$ORCH_IMAGE: not an image reference"
	[[ $REPO_URL =~ ^[^[:space:]\"\'\$\`\\]+$ ]] ||
		item blocked B0 "REPO_URL: must not contain spaces, quotes, '\$', '\`' or '\\'"
	# Never echo REPO_URL here: it would print the credential.
	if [[ $REPO_URL =~ ^[a-z+]+://[^/@]*:[^/@]*@ || $REPO_URL =~ ^https?://[^/@]+@ ]]; then
		item blocked B0 "REPO_URL embeds credentials; use the SSH URL (git@host:owner/repo.git) and the deploy key from B7"
	fi
	((BLOCKED > before)) || item ok B0 "inputs from $ENV_FILE are well formed"
}

# ---------------------------------------------------------------------------------------------------------
# step:B1 Media check
# MEDIA_STORAGE holds user data: only checked for existence, never created or chowned (D32).

check_b1() {
	if [[ -d $MEDIA_STORAGE ]]; then
		item ok B1 "MEDIA_STORAGE $MEDIA_STORAGE exists"
	else
		item warn B1 "MEDIA_STORAGE $MEDIA_STORAGE does not exist; bootstrap never creates it (D32), and the orchestrator app (B13) cannot be installed until it exists"
	fi
}

# ---------------------------------------------------------------------------------------------------------
# step:B2 Group
# A group with gid PGID, created as "homelab" (D32). An existing group with that gid is accepted as-is.

payload_group() {
	# payload:group
	cat <<EOF
[{"name": "homelab", "gid": ${PGID}, "smb": false}]
EOF
}

check_b2() {
	local found named
	found=$(mid group.query "[[\"gid\",\"=\",$PGID]]" | jq -r 'first | .name // empty')
	if [[ -n $found ]]; then
		item ok B2 "group gid $PGID exists ($found)"
		return 0
	fi
	named=$(mid group.query "[[\"name\",\"=\",\"$HOMELAB_NAME\"]]" | jq -r 'first | .gid // empty')
	if [[ -n $named ]]; then
		item blocked B2 "group $HOMELAB_NAME exists with gid $named, not PGID $PGID; set PGID=$named or rename that group"
	elif ((PGID < 1000 || PGID > 2999 || PGID == 950)); then
		item blocked B2 "PGID $PGID: a new group needs a gid in 1000-2999 other than 950"
	else
		item "will create" B2 "group $HOMELAB_NAME (gid $PGID, no SMB)"
		plan apply_b2
	fi
}

apply_b2() {
	local found
	found=$(mid group.query "[[\"gid\",\"=\",$PGID]]" | jq -r 'first | .name // empty')
	if [[ -n $found ]]; then
		say "B2 group gid $PGID: unchanged"
		return 0
	fi
	mid_payload group.create "$(payload_group)" >/dev/null
	say "B2 created group $HOMELAB_NAME (gid $PGID)"
}

# ---------------------------------------------------------------------------------------------------------
# step:B3 User
# A user with uid PUID, created as "homelab" with the PGID group as primary group (D32).

payload_user() {
	# payload:user
	cat <<EOF
[{"username": "homelab", "uid": ${PUID}, "full_name": "Homelab containers (truenas-orchestrator)", "group": ${GROUP_ROW_ID}, "group_create": false, "password_disabled": true, "smb": false, "shell": "/usr/sbin/nologin", "home": "/var/empty"}]
EOF
}

check_b3() {
	local found named
	found=$(mid user.query "[[\"uid\",\"=\",$PUID]]" | jq -r 'first | .username // empty')
	if [[ -n $found ]]; then
		item ok B3 "user uid $PUID exists ($found)"
		return 0
	fi
	named=$(mid user.query "[[\"username\",\"=\",\"$HOMELAB_NAME\"]]" | jq -r 'first | .uid // empty')
	if [[ -n $named ]]; then
		item blocked B3 "user $HOMELAB_NAME exists with uid $named, not PUID $PUID; set PUID=$named or rename that user"
	elif ((PUID < 1000 || PUID > 2999 || PUID == 950)); then
		item blocked B3 "PUID $PUID: a new user needs a uid in 1000-2999 other than 950"
	else
		item "will create" B3 "user $HOMELAB_NAME (uid $PUID, primary group gid $PGID, password disabled, no SMB, nologin)"
		plan apply_b3
	fi
}

apply_b3() {
	local GROUP_ROW_ID found
	found=$(mid user.query "[[\"uid\",\"=\",$PUID]]" | jq -r 'first | .username // empty')
	if [[ -n $found ]]; then
		say "B3 user uid $PUID: unchanged"
		return 0
	fi
	GROUP_ROW_ID=$(mid group.query "[[\"gid\",\"=\",$PGID]]" | jq -r 'first | .id // empty')
	[[ -n $GROUP_ROW_ID ]] || die "B3: no group with gid $PGID; B2 should have created it"
	mid_payload user.create "$(payload_user)" >/dev/null
	say "B3 created user $HOMELAB_NAME (uid $PUID)"
}

# ---------------------------------------------------------------------------------------------------------
# step:B4 Datasets
# HOMELAB_STORAGE, APPCONFIG_STORAGE, DATABASE_STORAGE and ORCH_STATE_STORAGE are datasets with the D32
# properties (DATABASE_STORAGE at recordsize 16K). Missing parents are created plain and inherit
# everything. An existing root keeps its properties: a difference is reported, never changed.

payload_dataset_parent() {
	local DATASET_NAME=$1
	# payload:dataset-parent
	cat <<EOF
[{"name": "${DATASET_NAME}", "type": "FILESYSTEM"}]
EOF
}

payload_dataset_root() {
	local DATASET_NAME=$1
	# payload:dataset-root
	cat <<EOF
[{"name": "${DATASET_NAME}", "type": "FILESYSTEM", "share_type": "GENERIC", "acltype": "POSIX", "aclmode": "DISCARD", "atime": "OFF", "compression": "LZ4", "recordsize": "128K"}]
EOF
}

payload_dataset_database() {
	local DATASET_NAME=$1
	# payload:dataset-database
	cat <<EOF
[{"name": "${DATASET_NAME}", "type": "FILESYSTEM", "share_type": "GENERIC", "acltype": "POSIX", "aclmode": "DISCARD", "atime": "OFF", "compression": "LZ4", "recordsize": "16K"}]
EOF
}

payload_dataset_state() {
	local DATASET_NAME=$1
	# payload:dataset-state
	cat <<EOF
[{"name": "${DATASET_NAME}", "type": "FILESYSTEM", "share_type": "GENERIC", "acltype": "POSIX", "aclmode": "DISCARD", "atime": "OFF", "compression": "LZ4", "recordsize": "128K"}]
EOF
}

# The four dataset roots, in creation order, with the payload function and record size of each.
readonly DATASET_ROOTS=(HOMELAB_STORAGE APPCONFIG_STORAGE DATABASE_STORAGE ORCH_STATE_STORAGE)

root_payload_fn() {
	case $1 in
	DATABASE_STORAGE) printf 'payload_dataset_database' ;;
	ORCH_STATE_STORAGE) printf 'payload_dataset_state' ;;
	*) printf 'payload_dataset_root' ;;
	esac
}

root_recordsize() {
	[[ $1 == DATABASE_STORAGE ]] && printf '16K' || printf '128K'
}

# Roots that B4 will create, so B5 and later checks can describe them before they exist.
declare -A WILL_CREATE=()

check_b4() {
	local k path diffs
	local -A parents_seen=()
	refresh_datasets
	for k in "${DATASET_ROOTS[@]}"; do
		path=${!k}
		resolve_root "$path"
		case $RES_STATE in
		dataset)
			diffs=$(dataset_prop_differences "$path" "$(root_recordsize "$k")")
			if [[ -n $diffs ]]; then
				item ok B4 "$k is dataset $RES_DATASET (differs, advisory, left as is: $diffs)"
			else
				item ok B4 "$k is dataset $RES_DATASET"
			fi
			;;
		plain)
			item ok B4 "$k: $RES_REASON (differs, advisory)"
			;;
		create)
			local p
			for p in "${RES_PARENTS[@]+"${RES_PARENTS[@]}"}"; do
				[[ -n ${parents_seen[$p]:-} ]] && continue
				parents_seen[$p]=1
				item "will create" B4 "parent dataset $p (plain, inherits everything)"
			done
			item "will create" B4 "$k dataset $RES_DATASET (POSIX ACL, aclmode discard, atime off, lz4, recordsize $(root_recordsize "$k"))"
			WILL_CREATE[$k]=1
			plan apply_b4
			;;
		blocked)
			item blocked B4 "$k: $RES_REASON"
			;;
		esac
	done
}

apply_b4() {
	local k path p fn
	for k in "${DATASET_ROOTS[@]}"; do
		path=${!k}
		refresh_datasets
		resolve_root "$path"
		case $RES_STATE in
		dataset | plain)
			say "B4 $k: unchanged"
			;;
		create)
			for p in "${RES_PARENTS[@]+"${RES_PARENTS[@]}"}"; do
				mid_payload pool.dataset.create "$(payload_dataset_parent "$p")" >/dev/null
				say "B4 created parent dataset $p"
			done
			fn=$(root_payload_fn "$k")
			mid_payload pool.dataset.create "$("$fn" "$RES_DATASET")" >/dev/null
			say "B4 created dataset $RES_DATASET for $k"
			;;
		*)
			die "B4 $k: $RES_REASON"
			;;
		esac
	done
}

# ---------------------------------------------------------------------------------------------------------
# step:B5 Permissions
# Each writable root directory is PUID:PGID 0750 (state: 0700) with no extended ACL, set with
# filesystem.setperm non-recursively: only the root directory changes, never its contents (D32).

payload_perm() {
	local PERM_PATH=$1
	# payload:perm
	cat <<EOF
[{"path": "${PERM_PATH}", "uid": ${PUID}, "gid": ${PGID}, "mode": "750", "options": {"stripacl": true, "recursive": false, "traverse": false}}]
EOF
}

payload_perm_state() {
	# payload:perm-state
	cat <<EOF
[{"path": "${ORCH_STATE_STORAGE}", "uid": ${PUID}, "gid": ${PGID}, "mode": "700", "options": {"stripacl": true, "recursive": false, "traverse": false}}]
EOF
}

readonly PERM_ROOTS=(HOMELAB_STORAGE APPCONFIG_STORAGE DATABASE_STORAGE ORCH_STATE_STORAGE)

perm_mode() {
	[[ $1 == ORCH_STATE_STORAGE ]] && printf '700' || printf '750'
}

# perm_current PATH: "uid:gid mode acl" of PATH, e.g. "0:0 755 false".
perm_current() {
	local out owner mode acl
	out=$(mid filesystem.stat "$(json_str "$1")" | jq -r '"\(.uid):\(.gid) \(.mode % 4096) \(.acl)"')
	read -r owner mode acl <<<"$out"
	printf '%s %o %s\n' "$owner" "$mode" "$acl"
}

check_b5() {
	local k path want cur
	for k in "${PERM_ROOTS[@]}"; do
		path=${!k}
		want="$PUID:$PGID $(perm_mode "$k") false"
		if [[ -n ${WILL_CREATE[$k]:-} ]]; then
			item "will change" B5 "$path: new dataset -> $PUID:$PGID $(perm_mode "$k"), no extended ACL"
			plan apply_b5
			continue
		fi
		[[ -d $path ]] || continue # blocked in B4
		cur=$(perm_current "$path")
		if [[ $cur == "$want" ]]; then
			item ok B5 "$path is $PUID:$PGID $(perm_mode "$k")"
		else
			item "will change" B5 "$path: ${cur% *} (extended ACL: ${cur##* }) -> $PUID:$PGID $(perm_mode "$k"), no extended ACL; contents untouched"
			plan apply_b5
		fi
	done
}

apply_b5() {
	local k path want cur
	for k in "${PERM_ROOTS[@]}"; do
		path=${!k}
		want="$PUID:$PGID $(perm_mode "$k") false"
		cur=$(perm_current "$path")
		if [[ $cur == "$want" ]]; then
			say "B5 $path: unchanged"
			continue
		fi
		if [[ $k == ORCH_STATE_STORAGE ]]; then
			mid_payload -j filesystem.setperm "$(payload_perm_state)" >/dev/null
		else
			mid_payload -j filesystem.setperm "$(payload_perm "$path")" >/dev/null
		fi
		say "B5 set $path to $PUID:$PGID $(perm_mode "$k")"
	done
}

# ---------------------------------------------------------------------------------------------------------
# step:B6 Networks
# home, proxy, isolated (internal) and databases exist, by name, however they were made. When none exists,
# or every one that exists belongs to the homelab-networks app, the app is created from
# homelab-networks.yaml (D33). A mix of hand-made and missing networks is blocking.

payload_networks_app() {
	local NETWORKS_COMPOSE_JSON
	NETWORKS_COMPOSE_JSON=$(jq -Rs '.' <"$SCRIPT_DIR/homelab-networks.yaml")
	# payload:networks-app
	cat <<EOF
[{"app_name": "homelab-networks", "custom_app": true, "custom_compose_config_string": ${NETWORKS_COMPOSE_JSON}}]
EOF
}

# networks_state: sets NET_MISSING (names), NET_FOREIGN (existing names not owned by the app), NET_APP
# (the app's state, empty when absent).
networks_state() {
	local nets n
	nets=$(mid docker.network.query '[["name","in",["home","proxy","isolated","databases"]]]')
	NET_MISSING=() NET_FOREIGN=()
	for n in "${HOMELAB_NETWORKS[@]}"; do
		case $(jq -r --arg n "$n" '
			map(select(.name == $n)) | first
			| if . == null then "missing"
			  elif ((.labels // {})["com.docker.compose.project"] // "") == "ix-homelab-networks" then "app"
			  else "foreign" end' <<<"$nets") in
		missing) NET_MISSING+=("$n") ;;
		foreign) NET_FOREIGN+=("$n") ;;
		esac
	done
	NET_APP=$(mid app.query "[[\"name\",\"=\",\"$NETWORKS_APP\"]]" '{"extra":{"retrieve_config":false}}' |
		jq -r 'first | .state // empty')
}

network_create_hint() {
	local n hint=""
	for n in "$@"; do
		if [[ $n == isolated ]]; then
			hint+="docker network create --internal isolated; "
		else
			hint+="docker network create $n; "
		fi
	done
	printf '%s' "${hint%; }"
}

check_b6() {
	networks_state
	if ((${#NET_MISSING[@]} == 0)); then
		item ok B6 "networks home, proxy, isolated, databases exist${NET_APP:+ (app $NETWORKS_APP: $NET_APP)}"
	elif ((${#NET_FOREIGN[@]} > 0)); then
		item blocked B6 "networks made by hand: ${NET_FOREIGN[*]}; missing: ${NET_MISSING[*]}. Compose cannot adopt hand-made networks, so create the missing ones by hand: $(network_create_hint "${NET_MISSING[@]}")"
	elif [[ -n $NET_APP ]]; then
		item blocked B6 "app $NETWORKS_APP exists ($NET_APP) but networks ${NET_MISSING[*]} are missing; start it in Apps"
	else
		item "will create" B6 "app $NETWORKS_APP from homelab-networks.yaml (networks ${NET_MISSING[*]})"
		plan apply_b6
	fi
}

apply_b6() {
	networks_state
	if ((${#NET_MISSING[@]} == 0)) || [[ -n $NET_APP ]]; then
		say "B6 networks: unchanged"
		return 0
	fi
	((${#NET_FOREIGN[@]} == 0)) || die "B6: networks ${NET_FOREIGN[*]} appeared since Check; re-run bootstrap"
	mid_payload -j app.create "$(payload_networks_app)" >/dev/null
	say "B6 created app $NETWORKS_APP"
}

# ---------------------------------------------------------------------------------------------------------
# step:B7 Deploy key
# An ed25519 key at $ORCH_STATE_STORAGE/ssh/id_ed25519 (0600) beside id_ed25519.pub (0644), owned by
# PUID:PGID. Never overwritten; the private half is never shown (D34).

deploy_key_path() {
	printf '%s/ssh/id_ed25519' "$ORCH_STATE_STORAGE"
}

# owner_mode PATH: "uid:gid mode" of PATH, e.g. "1000:1000 600".
owner_mode() {
	stat -c '%u:%g %a' -- "$1"
}

# deploy_key_drift: the deploy key paths whose owner or mode differ, as "path (now -> wanted)".
deploy_key_drift() {
	local key entry path want cur out=()
	key=$(deploy_key_path)
	for entry in "${key%/*}=700" "$key=600" "$key.pub=644"; do
		path=${entry%=*}
		want="$PUID:$PGID ${entry##*=}"
		cur=$(owner_mode "$path")
		[[ $cur == "$want" ]] || out+=("$path ($cur -> $want)")
	done
	printf '%s' "${out[*]+"${out[*]}"}"
}

fix_deploy_key_perms() {
	local key
	key=$(deploy_key_path)
	chown "$PUID:$PGID" "${key%/*}" "$key" "$key.pub"
	chmod 700 "${key%/*}"
	chmod 600 "$key"
	chmod 644 "$key.pub"
}

check_b7() {
	local key drift
	key=$(deploy_key_path)
	if [[ -f $key && -f $key.pub ]]; then
		drift=$(deploy_key_drift)
		if [[ -z $drift ]]; then
			item ok B7 "deploy key $key exists ($PUID:$PGID 600, .pub 644)"
		else
			item "will change" B7 "deploy key owner/mode: $drift"
			plan apply_b7
		fi
	elif [[ -e $key ]]; then
		item blocked B7 "$key exists without $key.pub; restore the public half (ssh-keygen -y -f $key > $key.pub) or remove both"
	else
		item "will create" B7 "deploy key $key (ed25519); its public half is printed for the repo's deploy keys"
		plan apply_b7
	fi
}

apply_b7() {
	local key dir drift
	key=$(deploy_key_path)
	dir=${key%/*}
	if [[ -e $key ]]; then
		drift=$(deploy_key_drift)
		if [[ -n $drift ]]; then
			fix_deploy_key_perms
			say "B7 set deploy key owner $PUID:$PGID, modes 700/600/644"
		else
			say "B7 deploy key: unchanged"
		fi
		return 0
	fi
	install -d -m 700 -o "$PUID" -g "$PGID" "$dir"
	ssh-keygen -q -t ed25519 -N '' -C "truenas-orchestrator@$(hostname)" -f "$key" >/dev/null
	fix_deploy_key_perms
	say "B7 created deploy key $key. Add this public key to $REPO_URL as a read-only deploy key:"
	printf '\n%s\n\n' "$(cat "$key.pub")"
	if [[ -n ${PLAN_HAS_B8:-} && -t 0 ]]; then
		read -r -p "  Press Enter once it is added as a read-only deploy key (the clone in B8 uses it) " _
	fi
}

# ---------------------------------------------------------------------------------------------------------
# step:B8 Repo
# HOMELAB_STORAGE is a clone of REPO_URL, owned by PUID:PGID. An empty root is cloned into; a clone of
# another remote, or a non-empty directory that is not a clone, is blocking.

git_ssh_command() {
	local dir=$ORCH_STATE_STORAGE/ssh
	printf 'ssh -i %s/id_ed25519 -o IdentitiesOnly=yes -o UserKnownHostsFile=%s/known_hosts -o StrictHostKeyChecking=accept-new' "$dir" "$dir"
}

repo_owner() {
	local om
	om=$(owner_mode "$HOMELAB_STORAGE/.git")
	printf '%s' "${om% *}"
}

repo_origin() {
	git -c safe.directory="$HOMELAB_STORAGE" -C "$HOMELAB_STORAGE" remote get-url origin 2>/dev/null || true
}

check_b8() {
	local origin owner
	if [[ -n ${WILL_CREATE[HOMELAB_STORAGE]:-} ]] || { [[ -d $HOMELAB_STORAGE ]] && [[ -z $(ls -A "$HOMELAB_STORAGE") ]]; }; then
		item "will create" B8 "clone $REPO_URL into $HOMELAB_STORAGE, owned by $PUID:$PGID"
		plan apply_b8
		PLAN_HAS_B8=1
		return 0
	fi
	[[ -d $HOMELAB_STORAGE ]] || return 0 # blocked in B4
	if [[ -d $HOMELAB_STORAGE/.git ]]; then
		origin=$(repo_origin)
		owner=$(repo_owner)
		if [[ $origin != "$REPO_URL" ]]; then
			item blocked B8 "$HOMELAB_STORAGE is a clone of ${origin:-an unknown remote}, not $REPO_URL; fix REPO_URL or the checkout"
		elif [[ $owner != "$PUID:$PGID" ]]; then
			item "will change" B8 "$HOMELAB_STORAGE/.git is owned by $owner; chown -R $PUID:$PGID $HOMELAB_STORAGE"
			plan apply_b8
		else
			item ok B8 "$HOMELAB_STORAGE is a clone of $REPO_URL, owned by $PUID:$PGID"
		fi
	else
		item blocked B8 "$HOMELAB_STORAGE is not empty and not a git clone; empty it or point HOMELAB_STORAGE elsewhere"
	fi
}

apply_b8() {
	local owner
	if [[ -d $HOMELAB_STORAGE/.git ]]; then
		owner=$(repo_owner)
		if [[ $owner != "$PUID:$PGID" ]]; then
			chown -R "$PUID:$PGID" "$HOMELAB_STORAGE"
			say "B8 set owner of $HOMELAB_STORAGE to $PUID:$PGID (recursive)"
		else
			say "B8 repo: unchanged"
		fi
		return 0
	fi
	[[ -z $(ls -A "$HOMELAB_STORAGE") ]] || die "B8: $HOMELAB_STORAGE is no longer empty; re-run bootstrap"
	# umask 022, the default of the root shell the hand-install guide uses, so both routes leave the
	# checkout with the same modes (M4 acceptance criterion 3).
	if ! (
		umask 022
		GIT_SSH_COMMAND=$(git_ssh_command) git -c safe.directory="$HOMELAB_STORAGE" clone --quiet -- "$REPO_URL" "$HOMELAB_STORAGE"
	); then
		die "B8: git clone of $REPO_URL failed. If it is a deploy-key error, add $(deploy_key_path).pub to the repo as a read-only deploy key and re-run"
	fi
	chown -R "$PUID:$PGID" "$HOMELAB_STORAGE"
	if [[ -f $ORCH_STATE_STORAGE/ssh/known_hosts ]]; then
		chown "$PUID:$PGID" "$ORCH_STATE_STORAGE/ssh/known_hosts"
		chmod 644 "$ORCH_STATE_STORAGE/ssh/known_hosts"
	fi
	say "B8 cloned $REPO_URL into $HOMELAB_STORAGE"
}

# ---------------------------------------------------------------------------------------------------------
# step:B9 known_hosts
# Written by the B8 clone (StrictHostKeyChecking=accept-new, trust on first use). Informational only.

check_b9() {
	local kh=$ORCH_STATE_STORAGE/ssh/known_hosts
	if [[ -f $kh ]]; then
		item ok B9 "$kh exists"
	elif [[ -n ${PLAN_HAS_B8:-} ]]; then
		item info B9 "$kh is written by the B8 clone (accept-new)"
	else
		item info B9 "$kh absent; the orchestrator's first fetch writes it (accept-new)"
	fi
}

# ---------------------------------------------------------------------------------------------------------
# step:B10 .global.env
# compose/.global.env exists and its six keys equal bootstrap.env. Absent: copied from
# compose/example.global.env with the keys set, 0600 PUID:PGID. Present and different: stop, never edit.

readonly GLOBAL_KEYS=(PUID PGID HOMELAB_STORAGE APPCONFIG_STORAGE DATABASE_STORAGE MEDIA_STORAGE)

global_env_path() {
	printf '%s/compose/.global.env' "$HOMELAB_STORAGE"
}

# dotenv_get FILE KEY: the last value of KEY in FILE, unquoted; empty when absent.
dotenv_get() {
	local line value=""
	while IFS= read -r line || [[ -n $line ]]; do
		line=${line%$'\r'}
		if [[ $line =~ ^[[:space:]]*(export[[:space:]]+)?$2[[:space:]]*=[[:space:]]*(.*)$ ]]; then
			value=${BASH_REMATCH[2]}
			if [[ $value =~ ^\"([^\"]*)\"[[:space:]]*(#.*)?$ || $value =~ ^\'([^\']*)\'[[:space:]]*(#.*)?$ ]]; then
				value=${BASH_REMATCH[1]}
			else
				# Unquoted: a " #" starts a comment, and trailing blanks are not part of the value.
				value=${value%%[[:space:]]#*}
				value=${value%"${value##*[![:space:]]}"}
			fi
		fi
	done <"$1"
	printf '%s' "$value"
}

# global_env_mismatches FILE: "KEY=found (bootstrap.env: want)" for each differing key.
global_env_mismatches() {
	local k got out=()
	for k in "${GLOBAL_KEYS[@]}"; do
		got=$(dotenv_get "$1" "$k")
		if [[ $got == *'$'* ]]; then
			out+=("$k=$got uses variable interpolation, which bootstrap does not resolve; write the literal value ${!k}")
		elif [[ $got != "${!k}" ]]; then
			out+=("$k=${got:-<unset>} (bootstrap.env: ${!k})")
		fi
	done
	printf '%s' "${out[*]+"${out[*]}"}"
}

check_b10() {
	local file mism om
	file=$(global_env_path)
	if [[ -n ${PLAN_HAS_B8:-} ]]; then
		item "will create" B10 "$file from compose/example.global.env with the six keys from $ENV_FILE (after the clone)"
		plan apply_b10
		return 0
	fi
	[[ -d $HOMELAB_STORAGE/.git ]] || return 0 # blocked in B8
	if [[ -f $file ]]; then
		mism=$(global_env_mismatches "$file")
		om=$(owner_mode "$file")
		if [[ -n $mism ]]; then
			item blocked B10 "$file differs from $ENV_FILE: $mism. Bootstrap never edits it; make the two agree by hand"
		elif [[ $om != "$PUID:$PGID 600" ]]; then
			item "will change" B10 "$file is $om; chown $PUID:$PGID, chmod 600 (content untouched)"
			plan apply_b10
		else
			item ok B10 "$file matches $ENV_FILE ($PUID:$PGID 600)"
		fi
	elif [[ -f $HOMELAB_STORAGE/compose/example.global.env ]]; then
		item "will create" B10 "$file from compose/example.global.env with the six keys from $ENV_FILE"
		plan apply_b10
	else
		item blocked B10 "neither $file nor compose/example.global.env exists in the repo"
	fi
}

apply_b10() {
	local file example tmp line k done_keys=" " mism om
	file=$(global_env_path)
	example=$HOMELAB_STORAGE/compose/example.global.env
	if [[ -f $file ]]; then
		mism=$(global_env_mismatches "$file")
		[[ -z $mism ]] || die "B10: $file differs from $ENV_FILE: $mism. Bootstrap never edits it; make the two agree by hand and re-run"
		om=$(owner_mode "$file")
		if [[ $om != "$PUID:$PGID 600" ]]; then
			chown "$PUID:$PGID" "$file"
			chmod 600 "$file"
			say "B10 set $file to $PUID:$PGID 600"
		else
			say "B10 .global.env: unchanged"
		fi
		return 0
	fi
	[[ -f $example ]] || die "B10: $example not found in the cloned repo; create $file by hand and re-run"
	tmp=$(mktemp "$TMP_DIR/global.env.XXXXXX")
	while IFS= read -r line || [[ -n $line ]]; do
		for k in "${GLOBAL_KEYS[@]}"; do
			if [[ $line =~ ^[[:space:]]*${k}[[:space:]]*= ]]; then
				line="$k=${!k}"
				done_keys+="$k "
				break
			fi
		done
		printf '%s\n' "$line"
	done <"$example" >"$tmp"
	for k in "${GLOBAL_KEYS[@]}"; do
		[[ $done_keys == *" $k "* ]] || printf '%s=%s\n' "$k" "${!k}" >>"$tmp"
	done
	install -m 600 -o "$PUID" -g "$PGID" "$tmp" "$file"
	say "B10 wrote $file"
}

# ---------------------------------------------------------------------------------------------------------
# step:B11 API key
# The orchestrator's TrueNAS API key, for TRUENAS_API_USER. Created only with --create-api-key, printed
# once to paste into the orchestrator's first-run wizard, and never written to disk.

payload_api_key() {
	# payload:api-key
	cat <<EOF
[{"name": "truenas-orchestrator", "username": "${TRUENAS_API_USER}"}]
EOF
}

api_key_count() {
	mid api_key.query "[[\"name\",\"=\",\"$API_KEY_NAME\"]]" | jq 'length'
}

check_b11() {
	local count users
	count=$(api_key_count)
	if ((count > 0)); then
		item ok B11 "API key $API_KEY_NAME exists (not shown; TrueNAS shows a key only once)"
	elif ((CREATE_API_KEY)); then
		users=$(mid user.query "[[\"username\",\"=\",\"$TRUENAS_API_USER\"]]" | jq 'length')
		if ((users == 0)); then
			item blocked B11 "TRUENAS_API_USER $TRUENAS_API_USER does not exist"
		else
			item "will create" B11 "API key $API_KEY_NAME for $TRUENAS_API_USER, printed once, never stored"
			plan apply_b11
		fi
	else
		item skip B11 "no API key $API_KEY_NAME; re-run with --create-api-key, or add one in Credentials > Users > $TRUENAS_API_USER > API Keys"
	fi
}

apply_b11() {
	local key count
	count=$(api_key_count)
	if ((count > 0)); then
		say "B11 API key: unchanged"
		return 0
	fi
	key=$(mid_payload api_key.create "$(payload_api_key)" | jq -r '.key // empty')
	[[ -n $key ]] || die "B11: api_key.create created $API_KEY_NAME but returned no key; delete it in Credentials > API Keys and re-run"
	say "B11 created API key $API_KEY_NAME for $TRUENAS_API_USER. It is shown once and not stored anywhere;"
	say "paste it into the orchestrator's first-run wizard (/environment):"
	printf '\n%s\n\n' "$key"
	key=""
}

# ---------------------------------------------------------------------------------------------------------
# step:B12 UI certificate
# The UI certificate's public PEM at $ORCH_STATE_STORAGE/truenas-ca.pem (0644), the orchestrator's
# ORCH_TRUENAS_CA_FILE. Refreshed when the certificate changes.

ca_path() {
	printf '%s/truenas-ca.pem' "$ORCH_STATE_STORAGE"
}

# ui_certificate_pem: only the public certificate leaves jq; the private key in the same object does not.
ui_certificate_pem() {
	mid system.general.config | jq -r '.ui_certificate.certificate // empty'
}

# Derived in B12 and rendered into compose.yaml in B13: how the orchestrator reaches and verifies the UI.
TRUENAS_UI_PORT=""
TRUENAS_TLS_SERVER_NAME=""
UI_CERT_SANS=""

# ui_tls_facts PEM: sets TRUENAS_UI_PORT from system.general.config, UI_CERT_SANS to the certificate's DNS
# names, and TRUENAS_TLS_SERVER_NAME to "localhost" when it is one of them (truenas_default), else the
# first DNS name that is not a wildcard; empty when there is none.
ui_tls_facts() {
	local pem=$1
	TRUENAS_UI_PORT=$(mid system.general.config | jq -r '.ui_httpsport // 443')
	[[ $TRUENAS_UI_PORT =~ ^[0-9]+$ ]] || die "B12: system.general.config ui_httpsport is not a port: $TRUENAS_UI_PORT"
	UI_CERT_SANS=$(printf '%s\n' "$pem" | openssl x509 -noout -ext subjectAltName 2>/dev/null |
		grep -o 'DNS:[^,[:space:]]*' | sed 's/^DNS://' || true)
	if grep -qx 'localhost' <<<"$UI_CERT_SANS"; then
		TRUENAS_TLS_SERVER_NAME=localhost
	else
		TRUENAS_TLS_SERVER_NAME=$(grep -v -m 1 '^\*' <<<"$UI_CERT_SANS" || true)
	fi
	[[ -z $TRUENAS_TLS_SERVER_NAME || $TRUENAS_TLS_SERVER_NAME =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] ||
		die "B12: the UI certificate's DNS name $TRUENAS_TLS_SERVER_NAME is not a host name"
}

# ca_state PEM: ok | change | create for the CA file against PEM, owner PUID:PGID and mode 0644.
ca_state() {
	local ca om
	ca=$(ca_path)
	if [[ ! -f $ca ]]; then
		printf 'create'
		return 0
	fi
	om=$(owner_mode "$ca")
	if [[ $(<"$ca") == "$1" && $om == "$PUID:$PGID 644" ]]; then
		printf 'ok'
	else
		printf 'change'
	fi
}

check_b12() {
	local pem ca state
	ca=$(ca_path)
	pem=$(ui_certificate_pem)
	if [[ -z $pem ]]; then
		item blocked B12 "system.general.config has no UI certificate; set one in System > General Settings > GUI"
		return 0
	fi
	ui_tls_facts "$pem"
	UI_CERT_SANS=${UI_CERT_SANS//$'\n'/ }
	state=$(ca_state "$pem")
	case $state in
	ok) item ok B12 "$ca matches the UI certificate ($PUID:$PGID 644)" ;;
	change)
		item "will change" B12 "$ca: rewrite from the UI certificate, $PUID:$PGID 0644 (now $(owner_mode "$ca"))"
		plan apply_b12
		;;
	create)
		item "will create" B12 "$ca from the UI certificate, $PUID:$PGID 0644"
		plan apply_b12
		;;
	esac
	item info B12 "UI on port $TRUENAS_UI_PORT; certificate DNS names: ${UI_CERT_SANS:-none}"
}

apply_b12() {
	local pem ca tmp state
	ca=$(ca_path)
	pem=$(ui_certificate_pem)
	[[ -n $pem ]] || die "B12: system.general.config has no UI certificate"
	state=$(ca_state "$pem")
	if [[ $state == ok ]]; then
		say "B12 $ca: unchanged"
		return 0
	fi
	tmp=$(mktemp "$TMP_DIR/ca.XXXXXX")
	printf '%s\n' "$pem" >"$tmp"
	install -m 644 -o "$PUID" -g "$PGID" "$tmp" "$ca"
	say "B12 wrote $ca"
}

# ---------------------------------------------------------------------------------------------------------
# step:B13 Orchestrator app
# The ORCH_APP_NAME custom app from compose.yaml, rendered with the bootstrap.env values. An existing app
# is never touched (D11).

readonly COMPOSE_VARS=(ORCH_IMAGE PUID PGID HOMELAB_STORAGE APPCONFIG_STORAGE DATABASE_STORAGE MEDIA_STORAGE
	ORCH_STATE_STORAGE ORCH_APP_NAME TRUENAS_UI_PORT TRUENAS_TLS_SERVER_NAME)

# render_compose: compose.yaml with every ${VAR} of COMPOSE_VARS replaced. Any other ${...} is an error.
# TRUENAS_UI_PORT and TRUENAS_TLS_SERVER_NAME come from B12; the rest from bootstrap.env.
render_compose() {
	local text v
	text=$(<"$SCRIPT_DIR/compose.yaml")
	for v in "${COMPOSE_VARS[@]}"; do
		text=${text//"\${$v}"/"${!v}"}
	done
	[[ $text != *"\${"* ]] || die "B13: $SCRIPT_DIR/compose.yaml has a placeholder bootstrap does not know"
	printf '%s\n' "$text"
}

payload_orchestrator_app() {
	local ORCH_COMPOSE_JSON
	ORCH_COMPOSE_JSON=$(render_compose | jq -Rs '.')
	# payload:orchestrator-app
	cat <<EOF
[{"app_name": "${ORCH_APP_NAME}", "custom_app": true, "custom_compose_config_string": ${ORCH_COMPOSE_JSON}}]
EOF
}

orch_app_state() {
	mid app.query "[[\"name\",\"=\",\"$ORCH_APP_NAME\"]]" '{"extra":{"retrieve_config":false}}' |
		jq -r 'first | .state // empty'
}

check_b13() {
	local state k before=$BLOCKED
	state=$(orch_app_state)
	if [[ -n $state ]]; then
		item ok B13 "app $ORCH_APP_NAME exists ($state); never modified by bootstrap or the orchestrator (D11)"
		return 0
	fi
	# The app bind-mounts every root with create_host_path: false, so Docker never creates one as root.
	for k in HOMELAB_STORAGE APPCONFIG_STORAGE DATABASE_STORAGE MEDIA_STORAGE ORCH_STATE_STORAGE; do
		if [[ ! -d ${!k} && -z ${WILL_CREATE[$k]:-} ]]; then
			item blocked B13 "$k ${!k} does not exist; the app mounts it with create_host_path: false, so create it (as a dataset, or a directory you own) and re-run"
		fi
	done
	if [[ -z $TRUENAS_TLS_SERVER_NAME ]]; then
		item blocked B13 "the UI certificate has no DNS name (wildcards do not count), so the orchestrator cannot verify TrueNAS's TLS certificate; select a UI certificate with a DNS subjectAltName in System > General Settings > GUI (truenas_default has localhost) and re-run"
	fi
	render_compose >/dev/null # a placeholder problem stops the run here, in Check
	if ((BLOCKED == before)); then
		item "will create" B13 "app $ORCH_APP_NAME from compose.yaml (image $ORCH_IMAGE, TrueNAS at wss://host.docker.internal:$TRUENAS_UI_PORT as $TRUENAS_TLS_SERVER_NAME, break-glass port 127.0.0.1:30808)"
		plan apply_b13
	fi
}

apply_b13() {
	local state
	state=$(orch_app_state)
	if [[ -n $state ]]; then
		say "B13 app $ORCH_APP_NAME: exists, not touched"
		return 0
	fi
	mid_payload -j app.create "$(payload_orchestrator_app)" >/dev/null
	say "B13 created app $ORCH_APP_NAME"
}

# ---------------------------------------------------------------------------------------------------------

main() {
	local f answer
	parse_args "$@"
	load_env "$ENV_FILE"

	printf 'Check (read-only)\n'
	check_b0
	if ((BLOCKED)); then
		printf '\nPreflight blocked: fix the items above and re-run.\n' >&2
		exit 1
	fi
	check_b1
	check_b2
	check_b3
	check_b4
	check_b5
	check_b6
	check_b7
	check_b8
	check_b9
	check_b10
	check_b11
	check_b12
	check_b13

	if ((BLOCKED)); then
		printf '\n%d blocked item(s): fix them and re-run. Nothing was changed.\n' "$BLOCKED" >&2
		exit 1
	fi
	if ((CHANGES == 0)); then
		printf '\nnothing to do\n'
		exit 0
	fi
	if ((DRY_RUN)); then
		printf '\nDry run: %d change(s) planned, nothing applied.\n' "$CHANGES"
		exit 0
	fi
	if ((!ASSUME_YES)); then
		[[ -t 0 ]] || die "stdin is not a terminal; re-run with --yes to apply without asking"
		read -r -p "$(printf '\nApply %d changes? [y/N] ' "$CHANGES")" answer
		[[ $answer == [yY] || $answer == [yY][eE][sS] ]] || {
			printf 'Aborted. Nothing was changed.\n'
			exit 1
		}
	fi

	TMP_DIR=$(mktemp -d)
	trap 'rm -rf -- "$TMP_DIR"' EXIT

	printf '\nApply\n'
	for f in "${PLAN[@]}"; do
		"$f"
	done
	printf '\nDone. Re-run bootstrap to confirm: it should print "nothing to do".\n'
	printf 'Then open the orchestrator (ssh -L 30808:127.0.0.1:30808 <this host>, http://localhost:30808)\n'
	printf 'and check /environment.\n'
}

main "$@"
