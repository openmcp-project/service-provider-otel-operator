#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and service-provider-otel-operator contributors
# SPDX-License-Identifier: Apache-2.0
# Best-effort diagnostics only: no Secrets, raw kubeconfigs, environment dumps, or full Docker inspect.
set -uo pipefail
umask 077

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
mode=${1:-snapshot}
case "$mode" in snapshot|host|watch|redact) ;; *) echo 'Usage: e2e-diagnostics.sh {snapshot|host|watch|redact}' >&2; exit 2 ;; esac
if command -v timeout >/dev/null 2>&1; then
  timeout_bin=timeout
elif command -v gtimeout >/dev/null 2>&1; then
  timeout_bin=gtimeout
else
  echo 'Diagnostics require GNU timeout or gtimeout' >&2
  exit 1
fi
command -v python3 >/dev/null 2>&1 || { echo 'Diagnostics require python3' >&2; exit 1; }
# Deliberately not beneath E2E_DIAGNOSTICS_DIR: this directory holds raw output and credentials.
private_dir=$(mktemp -d "${TMPDIR:-/tmp}/e2e-diagnostics-private.XXXXXXXX") || exit 1
artifact_root=${E2E_DIAGNOSTICS_DIR:-"$repo_root/.e2e-diagnostics"}
mkdir -p "$artifact_root" || { rm -rf "$private_dir"; exit 1; }
stamp=$(date -u +%Y%m%dT%H%M%SZ)-$$
artifact_dir="$artifact_root/$mode-$stamp"
mkdir -p "$artifact_dir" || { rm -rf "$private_dir"; exit 1; }
pids=()
current_pid=
cleanup() {
  trap - EXIT TERM INT
  if [[ -n "$current_pid" ]]; then kill "$current_pid" 2>/dev/null || true; fi
  for pid in "${pids[@]:-}"; do [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true; done
  if [[ -n "$current_pid" ]]; then wait "$current_pid" 2>/dev/null || true; fi
  for pid in "${pids[@]:-}"; do [[ -n "$pid" ]] && wait "$pid" 2>/dev/null || true; done
  rm -rf "$private_dir"
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT

# JSON is parsed before publishing. YAML emission needs no third-party Python packages.
# Text filtering is also used for live logs, so credentials never first land in artifacts.
cat > "$private_dir/sanitize.py" <<'PY'
import json
import re
import sys

sensitive = re.compile(r'kubeconfig|token|password|passwd|credential|authorization|bearer|client[-_]?key|client[-_]?certificate|certificate[-_]?authority|private[-_ ]?key|access[-_]?key|api[-_]?key', re.I)
long_value = re.compile(r'(?<![\w])[A-Za-z0-9+/=_-]{80,}(?![\w])')
userinfo = re.compile(r'(https?://)[^/\s@]+@', re.I)
bearer_value = re.compile(r'(?i)(bearer\s+)[^\s,;"\']+')
credential_value = re.compile(r'''(?i)((?:["']?)(?:kubeconfig|token|password|passwd|credential|authorization|client[-_]?key|client[-_]?certificate|certificate[-_]?authority|private[-_ ]?key|access[-_]?key|api[-_]?key)(?:[-_]?data)?["']?\s*[:=]\s*)("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,;}]+)''')
known_secret = re.compile(r'\b(?:gh[pousr]_|github_pat_|AKIA)[A-Za-z0-9_]+')

def text(line):
    if '-----BEGIN ' in line:
        return '[REDACTED PEM block]\n'
    line = bearer_value.sub(r'\1[REDACTED]', line)
    line = credential_value.sub(r'\1[REDACTED]', line)
    line = known_secret.sub('[REDACTED]', line)
    return long_value.sub('[REDACTED encoded value]', userinfo.sub(r'\1[REDACTED]@', line))

def clean(value):
    if isinstance(value, dict):
        result = {}
        for key, item in value.items():
            if key in ('managedFields', 'annotations', 'data', 'stringData', 'binaryData', 'env', 'envFrom', 'command', 'args') or sensitive.search(key):
                continue
            result[key] = clean(item)
        return result
    if isinstance(value, list):
        return [clean(item) for item in value[:100]]
    if isinstance(value, str):
        return text(value).rstrip('\n')
    return value

def scalar(value):
    return json.dumps(value, ensure_ascii=True)

def yaml(value, indent=0):
    prefix = ' ' * indent
    if isinstance(value, dict) and value:
        for key, item in value.items():
            if isinstance(item, (dict, list)) and item:
                print(prefix + scalar(key) + ':')
                yaml(item, indent + 2)
            else:
                print(prefix + scalar(key) + ': ' + scalar(item))
    elif isinstance(value, list) and value:
        for item in value:
            if isinstance(item, (dict, list)) and item:
                print(prefix + '-')
                yaml(item, indent + 2)
            else:
                print(prefix + '- ' + scalar(item))
    else:
        print(prefix + scalar(value))

if sys.argv[1] == 'resources':
    raw = sys.stdin.read(4 * 1024 * 1024 + 1)
    if len(raw) > 4 * 1024 * 1024:
        print('Diagnostic resource output exceeded 4 MiB; omitted')
        sys.exit(1)
    try:
        print('# Credential-bearing fields omitted; arrays limited to 100 entries.')
        yaml(clean(json.loads(raw)))
    except (ValueError, TypeError):
        print('Diagnostic resource output was not valid JSON; omitted (see command stderr)')
        sys.exit(1)
else:
    pem = False
    block_indent = None
    for line in sys.stdin:
        indent = len(line) - len(line.lstrip())
        if block_indent is not None:
            if not line.strip() or indent > block_indent:
                continue
            block_indent = None
        if sensitive.search(line) and re.search(r':\s*[|>]?[+-]?\s*$', line):
            block_indent = indent
        if '-----BEGIN ' in line:
            pem = True
            print('[REDACTED PEM block]', flush=True)
        elif pem:
            if '-----END ' in line:
                pem = False
        else:
            print(text(line), end='', flush=True)
PY

# Budget includes discovery, not just Kubernetes calls. Individual commands never outlive it.
started=$SECONDS
budget=120
failures=0
sequence=0
bounded() {
  local seconds=$1
  shift
  if [[ "$mode" != watch && "$mode" != redact ]]; then
    local remaining=$((budget - SECONDS + started))
    if ((remaining <= 0)); then
      printf 'Diagnostic snapshot budget exhausted\n' >&2
      return 124
    fi
    if ((seconds > remaining)); then seconds=$remaining; fi
  fi
  "$timeout_bin" --signal=TERM --kill-after=2s "${seconds}s" "$@" <&0 &
  current_pid=$!
  local status=0
  wait "$current_pid" || status=$?
  current_pid=
  return "$status"
}
capture() {
  local title=$1 format=$2 seconds=$3
  shift 3
  local remaining=$((budget - SECONDS + started)) status=0
  if ((remaining <= 0)); then
    printf 'Diagnostic snapshot budget exhausted; skipping %s\n' "$title"
    return 0
  fi
  if ((seconds > remaining)); then seconds=$remaining; fi
  sequence=$((sequence + 1))
  local raw="$private_dir/output-$sequence" err="$private_dir/error-$sequence"
  local target="$artifact_dir/$title"
  printf '\n===== %s =====\n' "$title"
  bounded "$seconds" "$@" > "$raw" 2> "$err" || status=$?
  python3 "$private_dir/sanitize.py" "$format" < "$raw" | tee "$target" || status=1
  if [[ -s "$err" ]]; then
    python3 "$private_dir/sanitize.py" text < "$err" | tee -a "$target"
  fi
  if ((status != 0)); then
    printf 'Diagnostic command exited %s: %s\n' "$status" "$title" | tee -a "$target"
    failures=$((failures + 1))
  fi
  rm -f "$raw" "$err"
}

host_snapshot() {
  capture host-os.txt text 5 bash -c 'uname -a; if [[ -r /etc/os-release ]]; then cat /etc/os-release; else sw_vers; fi; printf "\nResource limits (including RLIMIT_NOFILE):\n"; ulimit -a; printf "\nHard nofile limit: "; ulimit -Hn'
  capture host-inotify.txt text 5 sysctl fs.inotify.max_user_instances fs.inotify.max_user_watches fs.inotify.max_queued_events
  capture host-inotify-usage.txt text 8 python3 -c '
import collections, os
counts = collections.Counter()
unreadable = 0
for process in os.scandir("/proc"):
    if not process.name.isdigit():
        continue
    try:
        uid = process.stat().st_uid
        for fd in os.scandir(process.path + "/fd"):
            try:
                if "inotify" in os.readlink(fd.path):
                    counts[uid] += 1
            except OSError:
                unreadable += 1
    except OSError:
        unreadable += 1
print("Current UID:", os.getuid())
print("Observed inotify descriptor references per UID (shared/duplicated FDs may overcount instances):")
for uid, count in sorted(counts.items()):
    print("uid=%s inotify_fd_references=%s" % (uid, count))
print("Unreadable or vanished process/fd entries:", unreadable)
'
  capture host-memory.txt text 5 bash -c 'if command -v free >/dev/null; then free -h; else vm_stat; fi'
  capture host-disk.txt text 5 df -h
  capture host-inodes.txt text 5 df -i
  capture docker-version.txt text 8 docker version
  capture docker-info.txt text 8 docker info
  capture docker-containers.txt text 8 docker ps -a --no-trunc
  capture docker-networks.txt text 5 docker network ls
}

discover_nodes() {
  bounded 5 docker ps -a --filter label=io.x-k8s.kind.cluster --format '{{.ID}} {{.Names}} {{.Label "io.x-k8s.kind.cluster"}}'
}
node_snapshot() {
  local id=$1 name=$2
  capture "node-$name-state.txt" text 5 docker inspect --format '{{json .State}} RestartCount={{.RestartCount}}' "$id"
  capture "node-$name-startup.txt" text 8 docker logs --timestamps --tail 400 "$id"
  capture "node-$name-limits.txt" text 5 docker exec "$id" sh -c 'ulimit -a; sysctl fs.inotify.max_user_instances fs.inotify.max_user_watches fs.inotify.max_queued_events; free -h; df -h; df -i'
  capture "node-$name-journals.txt" text 8 docker exec "$id" journalctl --no-pager -u kubelet -u containerd --since=-15min --lines=400
}

snapshot() {
  host_snapshot
  local nodes="$private_dir/nodes" clusters="$private_dir/clusters" records="$private_dir/records"
  discover_nodes > "$nodes" 2> "$private_dir/discovery-error" || printf 'Docker kind-node discovery failed\n'
  python3 "$private_dir/sanitize.py" text < "$private_dir/discovery-error"
  bounded 5 kind get clusters > "$clusters" 2> "$private_dir/discovery-error" || printf 'kind cluster discovery failed; using Docker labels\n'
  python3 "$private_dir/sanitize.py" text < "$private_dir/discovery-error"
  local id name cluster seen=' ' count=0 config resources resource available
  while read -r id name cluster; do
    [[ -n "$cluster" ]] && printf '%s\n' "$cluster" >> "$clusters"
  done < "$nodes"
  : > "$records"
  # CR state precedes pod logs: startup and pre-cleanup reconciliation evidence has priority.
  while read -r cluster; do
    [[ "$cluster" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || continue
    [[ "$seen" == *" $cluster "* ]] && continue
    seen="$seen$cluster "
    count=$((count + 1))
    if ((count > 8 || SECONDS - started >= budget)); then break; fi
    config="$private_dir/kubeconfig-$count"
    if ! bounded 8 kind get kubeconfig --name "$cluster" > "$config" 2> "$private_dir/kubeconfig-error"; then
      printf 'Could not obtain private kubeconfig for cluster %s\n' "$cluster"
      python3 "$private_dir/sanitize.py" text < "$private_dir/kubeconfig-error"
      continue
    fi
    chmod 600 "$config"
    printf '%s %s\n' "$cluster" "$config" >> "$records"
    available="$private_dir/resources-$count"
    if ! bounded 8 kubectl --kubeconfig "$config" --request-timeout=6s api-resources --verbs=list -o name > "$available" 2> "$private_dir/api-error"; then
      printf 'API discovery failed for cluster %s\n' "$cluster"
      python3 "$private_dir/sanitize.py" text < "$private_dir/api-error"
    fi
    resources=
    while read -r resource; do
      case "$resource" in
        clusters.*|clusterrequests.*|accessrequests.*|oteloperators.*|controlplanes.*|openmcpoperators.*|clusterproviders.*|serviceproviders.*|providerconfigs.*|*.source.toolkit.fluxcd.io|*.helm.toolkit.fluxcd.io|*.kustomize.toolkit.fluxcd.io)
          resources="${resources:+$resources,}$resource" ;;
      esac
    done < "$available"
    if [[ -n "$resources" ]]; then
      capture "cluster-$cluster-resources.yaml" resources 12 kubectl --kubeconfig "$config" --request-timeout=8s get "$resources" -A -o json --chunk-size=100
    fi
    capture "cluster-$cluster-workloads.txt" text 10 kubectl --kubeconfig "$config" --request-timeout=8s get pods,nodes,deployments -A -o wide
    capture "cluster-$cluster-events.txt" text 8 kubectl --kubeconfig "$config" --request-timeout=6s get events -A --sort-by=.metadata.creationTimestamp
  done < "$clusters"
  count=0
  while read -r id name cluster; do
    [[ "$name" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || continue
    count=$((count + 1))
    ((count <= 16)) || break
    node_snapshot "$id" "$name"
  done < "$nodes"
  local pods namespace pod logs=0
  while read -r cluster config; do
    pods="$private_dir/pods"
    bounded 8 kubectl --kubeconfig "$config" --request-timeout=6s get pods -A -o 'custom-columns=NS:.metadata.namespace,NAME:.metadata.name' --no-headers > "$pods" 2> "$private_dir/pod-error" || printf 'Controller pod discovery failed for %s\n' "$cluster"
    python3 "$private_dir/sanitize.py" text < "$private_dir/pod-error"
    while read -r namespace pod; do
      case "$namespace/$pod" in
        *openmcp*|*open-mcp*|*cluster-provider*|*service-provider*|*otel-operator*|flux-system/*|*flux*controller*) ;;
        *) continue ;;
      esac
      logs=$((logs + 1))
      if ((logs > 20 || SECONDS - started >= budget)); then break; fi
      capture "cluster-$cluster-$namespace-$pod.log" text 8 kubectl --kubeconfig "$config" --request-timeout=6s logs -n "$namespace" "$pod" --all-containers --timestamps --tail=300 --limit-bytes=131072
      capture "cluster-$cluster-$namespace-$pod-previous.log" text 5 kubectl --kubeconfig "$config" --request-timeout=4s logs -n "$namespace" "$pod" --all-containers --previous --timestamps --tail=100 --limit-bytes=65536
    done < "$pods"
  done < "$records"
  printf '\nDiagnostics finished in %ss; failed commands: %s; artifacts: %s\n' "$((SECONDS - started))" "$failures" "$artifact_dir"
}

# Each nested worker owns its own child list and traps. GNU timeout forwards TERM to
# its command group; the top-level watcher waits for workers before removing private files.
follow_node() {
  pids=()
  current_pid=
  trap cleanup_worker EXIT
  trap 'exit 143' TERM
  trap 'exit 130' INT
  local id=$1 target=$2 since=$3 status next_since fifo="$private_dir/follow-$1"
  mkfifo "$fifo"
  while :; do
    python3 "$private_dir/sanitize.py" text < "$fifo" >> "$target" &
    pids=("$!")
    status=0
    next_since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    bounded 60 docker logs --follow --timestamps --tail=400 --since "$since" "$id" > "$fifo" 2>&1 || status=$?
    wait "${pids[0]}" 2>/dev/null || true
    pids=()
    if ((status != 0 && status != 124)); then
      printf 'Docker startup log follower exited %s for %s\n' "$status" "$id" >> "$target"
    fi
    bounded 3 docker inspect --format '{{.Id}}' "$id" >/dev/null 2>&1 || break
    # Replaying one bounded time window is preferable to losing startup errors on reconnect.
    since=$next_since
    read -r -t 1 -u 4 _ || true
  done
}
cleanup_worker() {
  trap - EXIT TERM INT
  if [[ -n "$current_pid" ]]; then kill "$current_pid" 2>/dev/null || true; fi
  for pid in "${pids[@]:-}"; do [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true; done
  if [[ -n "$current_pid" ]]; then wait "$current_pid" 2>/dev/null || true; fi
  for pid in "${pids[@]:-}"; do [[ -n "$pid" ]] && wait "$pid" 2>/dev/null || true; done
}
watch_node() {
  pids=()
  current_pid=
  trap cleanup_worker EXIT
  trap 'exit 143' TERM
  trap 'exit 130' INT
  local id=$1 name=$2 since=$3 raw="$private_dir/journal-$1"
  follow_node "$id" "$artifact_dir/node-$name-$id-startup.log" "$since" &
  pids=("$!")
  while :; do
    {
      date -u +%Y-%m-%dT%H:%M:%SZ
      bounded 5 docker inspect --format '{{json .State}} RestartCount={{.RestartCount}}' "$id"
      bounded 5 docker exec "$id" sh -c 'printf "nofile soft: "; ulimit -Sn; printf "nofile hard: "; ulimit -Hn; sysctl fs.inotify.max_user_instances fs.inotify.max_user_watches fs.inotify.max_queued_events'
      bounded 8 docker exec "$id" journalctl --no-pager -u kubelet -u containerd --since=-30s --lines=200
    } > "$raw" 2>&1
    python3 "$private_dir/sanitize.py" text < "$raw" >> "$artifact_dir/node-$name-$id-journals.log"
    bounded 3 docker inspect --format '{{.Id}}' "$id" >/dev/null 2>&1 || break
    read -r -t 3 -u 4 _ || true
  done
  # Let the follower flush the final Docker output after removal before reaping it.
  wait "${pids[0]}" 2>/dev/null || true
  pids=()
}
watch() {
  local fifo="$private_dir/events" pause="$private_dir/pause" node_list="$private_dir/watch-nodes"
  local id name cluster seen=' ' count=0 since
  since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  mkfifo "$fifo" "$pause"
  exec 3<> "$fifo"
  exec 4<> "$pause"
  # Start the event stream before inventory so short-lived create/start events are retained.
  "$timeout_bin" --signal=TERM --kill-after=2s 2h docker events --since "$since" --filter label=io.x-k8s.kind.cluster --filter event=create --filter event=start --format '{{.Actor.ID}}' > "$fifo" 2> "$private_dir/events-error" &
  pids+=("$!")
  printf 'Watching kind nodes; retained artifacts: %s\n' "$artifact_dir"
  while :; do
    discover_nodes > "$node_list" 2> "$private_dir/watch-discovery-error" || {
      python3 "$private_dir/sanitize.py" text < "$private_dir/watch-discovery-error" >> "$artifact_dir/watcher-errors.log"
    }
    if read -r -t 1 -u 3 id; then
      bounded 3 docker inspect --format '{{.Id}} {{.Name}} {{index .Config.Labels "io.x-k8s.kind.cluster"}}' "$id" >> "$node_list" 2> "$private_dir/watch-inspect-error" || {
        printf 'Kind node %s disappeared before inspection\n' "$id" >> "$artifact_dir/watcher-errors.log"
        # Docker may still retain the last logs between exit and removal.
        bounded 3 docker logs --timestamps --tail 400 "$id" > "$private_dir/last-logs" 2>&1 || true
        python3 "$private_dir/sanitize.py" text < "$private_dir/last-logs" >> "$artifact_dir/node-$id-startup.log"
      }
    fi
    while read -r id name cluster; do
      [[ "$id" =~ ^[a-f0-9]+$ ]] || continue
      # Canonical short IDs avoid spawning twice for Docker ps versus event inventory.
      id=${id:0:12}
      [[ "$seen" == *" $id "* ]] && continue
      name=${name#/}
      [[ "$name" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || name=$id
      count=$((count + 1))
      if ((count > 32)); then
        printf 'Watcher node limit (32) reached\n' >> "$artifact_dir/watcher-errors.log"
        break
      fi
      seen="$seen$id "
      watch_node "$id" "$name" "$since" &
      pids+=("$!")
    done < "$node_list"
    if [[ -s "$private_dir/events-error" ]]; then
      python3 "$private_dir/sanitize.py" text < "$private_dir/events-error" >> "$artifact_dir/watcher-errors.log"
      : > "$private_dir/events-error"
    fi
  done
}

case "$mode" in
  host) host_snapshot; printf 'Host diagnostic command failures: %s\n' "$failures" ;;
  snapshot) snapshot ;;
  watch) watch ;;
  redact) bounded 7200 python3 "$private_dir/sanitize.py" text ;;
esac
