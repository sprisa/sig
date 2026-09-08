#!/usr/bin/env bash

# Never trace commands: the inherited environment contains live credentials.
set +x
set -euo pipefail
umask 077

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

for dependency in go jq; do
  command -v "$dependency" >/dev/null 2>&1 || fail "$dependency is required"
done
[[ -n ${SIGNOZ_URL:-} ]] || fail 'SIGNOZ_URL must be supplied in the environment'
[[ -n ${SIGNOZ_API_KEY:-} ]] || fail 'SIGNOZ_API_KEY must be supplied in the environment'
[[ -z ${SIG_E2E_START:-} && -z ${SIG_E2E_END:-} || -n ${SIG_E2E_START:-} && -n ${SIG_E2E_END:-} ]] ||
  fail 'supply both SIG_E2E_START and SIG_E2E_END, or neither'

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
workdir=$(mktemp -d "${TMPDIR:-/tmp}/sig-e2e.XXXXXXXX")
export SIG_CONFIG_DIR="$workdir/config"
stdout="$workdir/stdout"
stderr="$workdir/stderr"
needs_logout=0

cleanup() {
  result=$?
  trap - EXIT
  if [[ $needs_logout == 1 && -f $SIG_CONFIG_DIR/config.json ]]; then
    if ! go run . auth logout >"$stdout" 2>"$stderr"; then
      printf 'FAIL: temporary credential cleanup failed. Configuration retained at %s; retry auth logout with SIG_CONFIG_DIR set to that directory.\n' "$SIG_CONFIG_DIR" >&2
      exit 1
    fi
  fi
  rm -rf -- "${workdir:?}"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

run_ok() {
  local label=$1
  shift
  printf 'CHECK: %s\n' "$label"
  if ! go run . "$@" >"$stdout" 2>"$stderr"; then
    # Report only CLI-defined error codes, never telemetry or upstream bodies.
    local code
    code=$(jq -Rrs 'split("\n")[0] | fromjson? | .error.code // empty' "$stderr" 2>/dev/null) || code=''
    case "$code" in
      authentication|permission|network|timeout|config|credentials|invalid_query|invalid_response|api)
        fail "$label failed ($code); response contents suppressed for privacy" ;;
      *) fail "$label failed; response contents suppressed for privacy" ;;
    esac
  fi
  [[ ! -s $stderr ]] || fail "$label wrote unexpected stderr"
  jq -es 'length == 1 and (.[0] | type == "object" and has("data"))' "$stdout" >/dev/null 2>&1 ||
    fail "$label did not return one JSON result"
}

assert_logs() {
  jq -e '
    (.data | type == "array") and
    (.meta.signal == "logs") and
    (.meta.returned == (.data | length)) and
    (.meta.limit == 5) and (.meta.returned <= 5) and
    (.meta.completeness as $c | ["complete", "unknown", "more_available"] | index($c) != null) and
    (.meta.next_cursor | type == "string") and
    (.data | all(.[]; (.timestamp | type == "string") and (.data | type == "object")))
  ' "$stdout" >/dev/null 2>&1 || fail 'log result shape or bounded-result metadata is incorrect'
}

run_ok 'Go build and JSON version output' version
jq -e '.data.version | type == "string"' "$stdout" >/dev/null 2>&1 || fail 'missing CLI version'

# Login uses the environment key and creates only an isolated keychain reference.
needs_logout=1
run_ok 'service-account login' auth login
jq -e '.data.authenticated == true and .data.context == "default" and .data.credential_source == "keychain"' "$stdout" >/dev/null 2>&1 ||
  fail 'login did not create an authenticated default context'

# Clear the environment override only in this subshell to prove keychain retrieval.
(
  unset SIGNOZ_API_KEY
  run_ok 'stored-key authentication and identity' auth status
  jq -e '.data.authenticated == true and .data.credential_source == "keychain" and (.data.identity.id | type == "string" and length > 0)' "$stdout" >/dev/null 2>&1 ||
    fail 'stored-key identity validation failed'
)
identity=$(jq -c '.data.identity.id' "$stdout" 2>/dev/null)

run_ok 'environment-key authentication and identity' auth status
jq -e --argjson expected "$identity" '.data.authenticated == true and .data.credential_source == "environment" and .data.identity.id == $expected' "$stdout" >/dev/null 2>&1 ||
  fail 'environment and keychain credentials returned different identities'

run_ok 'recent logs, at most five records' logs search --since 15m --limit 5
assert_logs

run_ok 'native error filter, at most five records' logs search --where "severity_text = 'ERROR'" --since 1h --limit 5
assert_logs

# Freeze a window for reproducible UI comparison. Relative defaults are one hour.
end=${SIG_E2E_END:-$(jq -nr 'now | floor | strftime("%Y-%m-%dT%H:%M:%SZ")')}
start=${SIG_E2E_START:-$(jq -nr --arg end "$end" '$end | fromdateiso8601 - 3600 | strftime("%Y-%m-%dT%H:%M:%SZ")')}
query=(logs search --start "$start" --end "$end" --limit 5)
if [[ -n ${SIG_E2E_WHERE:-} ]]; then
  query+=(--where "$SIG_E2E_WHERE")
fi
run_ok 'known-window log retrieval' "${query[@]}"
assert_logs
jq -e '.meta.returned > 0' "$stdout" >/dev/null 2>&1 ||
  fail 'no logs in the comparison window; supply SIG_E2E_START/END and optionally SIG_E2E_WHERE for known data'
jq -e '.meta.warning == null' "$stdout" >/dev/null 2>&1 ||
  fail 'comparison query returned a warning; use a window and filter that return an unqualified result'

# Go serializes these timestamps as UTC RFC3339. Pad fractions before comparing
# so timestamps with and without fractional seconds sort consistently.
jq -e '
  def timestamp_key:
    capture("^(?<seconds>[0-9T:-]+)(?:\\.(?<fraction>[0-9]+))?Z$") |
    .seconds + (((.fraction // "") + "000000000")[0:9]);
  (.meta.start | timestamp_key) as $start |
  (.meta.end | timestamp_key) as $end |
  [.data[].timestamp | timestamp_key] as $times |
  ($times == ($times | sort | reverse)) and
  all($times[]; . >= $start and . <= $end)
' "$stdout" >/dev/null 2>&1 || fail 'comparison logs are out of time bounds or not newest-first'

if [[ -n ${SIG_E2E_EXPECT_ID:-} ]]; then
  jq -e 'any(.data[]; .data.id == env.SIG_E2E_EXPECT_ID)' "$stdout" >/dev/null 2>&1 ||
    fail 'expected log ID was not present in the five returned records'
  printf 'PASS: expected log ID from the UI was returned\n'
else
  printf 'NOTE: UI content/ID comparison is not automated without SIG_E2E_EXPECT_ID; no telemetry is printed\n'
fi

printf 'CHECK: invalid credential rejection\n'
status=0
SIGNOZ_API_KEY=invalid-test-key go run . auth status >"$stdout" 2>"$stderr" || status=$?
[[ $status == 1 && ! -s $stdout ]] || fail 'invalid credential unexpectedly succeeded or wrote stdout'
# go run maps the program exit code to 1 and appends "exit status 3" to stderr.
jq -Rse '
  split("\n") | map(select(length > 0)) |
  length == 2 and .[1] == "exit status 3" and
  (.[0] | fromjson | .error.code == "authentication" and .error.http_status == 401)
' "$stderr" >/dev/null 2>&1 || fail 'invalid credential did not produce the expected JSON 401 and application exit code 3'

run_ok 'valid credential still works after the negative test' auth status
run_ok 'remove temporary keychain credential' auth logout
jq -e '.data.credential_removed == true and .data.server_key_revoked == false' "$stdout" >/dev/null 2>&1 ||
  fail 'logout did not confirm local-only credential removal'
needs_logout=0

printf 'PASS: live smoke tests completed; temporary responses and configuration will be removed\n'
