package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the Bash workflow without a live server, Go subprocess builds, or an
// OS keychain. The CLI itself has separate HTTP integration tests.
func TestE2EScript(t *testing.T) {
	for _, dependency := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(dependency); err != nil {
			t.Skipf("script tests require %s", dependency)
		}
	}
	for _, tt := range []struct {
		name, scenario, expectedID, want string
		fail, retained                   bool
	}{
		{name: "success", want: "PASS: live smoke tests completed"},
		{name: "known ID", expectedID: "synthetic-log-id", want: "PASS: expected log ID from the UI was returned"},
		{name: "wrong ID", expectedID: "different-id", want: "expected log ID was not present", fail: true},
		{name: "empty data", scenario: "empty", want: "no logs in the comparison window", fail: true},
		{name: "wrong keychain source", scenario: "source", want: "stored-key identity validation failed", fail: true},
		{name: "wrong identity", scenario: "identity", want: "different identities", fail: true},
		{name: "out of bounds", scenario: "time", want: "out of time bounds", fail: true},
		{name: "invalid key accepted", scenario: "accepted", want: "invalid credential unexpectedly succeeded", fail: true},
		{name: "redirect not unauthorized", scenario: "redirect", want: "expected JSON 401", fail: true},
		{name: "cleanup fails", scenario: "cleanup", want: "temporary credential cleanup failed", fail: true, retained: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			temp := t.TempDir()
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(stubGo), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "e2e.sh")
			for _, value := range os.Environ() {
				key, _, _ := strings.Cut(value, "=")
				if key == "PATH" || key == "TMPDIR" || strings.HasPrefix(key, "SIGNOZ_") || strings.HasPrefix(key, "SIG_") {
					continue
				}
				cmd.Env = append(cmd.Env, value)
			}
			cmd.Env = append(cmd.Env,
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TMPDIR="+temp,
				"SIGNOZ_URL=https://signoz.example.com",
				"SIGNOZ_API_KEY=synthetic-api-key",
				"SIG_E2E_START=2026-01-01T00:00:00Z",
				"SIG_E2E_END=2026-01-01T01:00:00Z",
				"SIG_E2E_EXPECT_ID="+tt.expectedID,
				"SIG_STUB_SCENARIO="+tt.scenario,
				"SIG_STUB_JOURNAL="+filepath.Join(temp, "journal"),
			)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tt.fail || !strings.Contains(string(out), tt.want) {
				t.Fatalf("error=%v, expected failure=%v and %q; output:\n%s", err, tt.fail, tt.want, out)
			}
			for _, private := range []string{"signoz.example.com", "synthetic-api-key", "synthetic-account-id", "synthetic-log-id", "synthetic-body"} {
				if strings.Contains(string(out), private) {
					t.Fatalf("script printed private response or environment data: %q", private)
				}
			}
			files, err := filepath.Glob(filepath.Join(temp, "sig-e2e.*"))
			if err != nil {
				t.Fatal(err)
			}
			if (len(files) > 0) != tt.retained {
				t.Fatalf("temporary files retained=%v, want %v", files, tt.retained)
			}
			journal, err := os.ReadFile(filepath.Join(temp, "journal"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(journal), "auth logout") {
				t.Fatal("temporary credential cleanup was not attempted")
			}
		})
	}
}

const stubGo = `#!/usr/bin/env bash
set -euo pipefail
[[ $1 == run && $2 == . ]] || exit 99
shift 2
printf '%s\n' "$*" >>"$SIG_STUB_JOURNAL"
case "$*" in
  version)
    printf '%s\n' '{"data":{"version":"test"}}'
    ;;
  'auth login')
    mkdir -p "$SIG_CONFIG_DIR"
    printf '%s\n' '{}' >"$SIG_CONFIG_DIR/config.json"
    printf '%s\n' '{"data":{"authenticated":true,"context":"default","credential_source":"keychain"}}'
    ;;
  'auth status')
    if [[ ${SIGNOZ_API_KEY:-} == invalid-test-key && $SIG_STUB_SCENARIO != accepted ]]; then
      status=401
      [[ $SIG_STUB_SCENARIO != redirect ]] || status=302
      printf '{"error":{"code":"authentication","http_status":%s}}\nexit status 3\n' "$status" >&2
      exit 1
    fi
    source=environment
    [[ -n ${SIGNOZ_API_KEY:-} ]] || source=keychain
    [[ $SIG_STUB_SCENARIO != source ]] || source=environment
    identity=synthetic-account-id
    if [[ $SIG_STUB_SCENARIO == identity && $source == environment ]]; then identity=other-account; fi
    printf '{"data":{"authenticated":true,"credential_source":"%s","identity":{"id":"%s"}}}\n' "$source" "$identity"
    ;;
  logs\ search*)
    count=1
    timestamp=2026-01-01T00:30:00.123Z
    [[ $SIG_STUB_SCENARIO != time ]] || timestamp=2025-01-01T00:30:00Z
    rows='[{"timestamp":"'"$timestamp"'","data":{"id":"synthetic-log-id","body":"synthetic-body"}}]'
    if [[ $SIG_STUB_SCENARIO == empty ]]; then count=0; rows='[]'; fi
    printf '{"data":%s,"meta":{"signal":"logs","returned":%s,"limit":5,"completeness":"complete","next_cursor":"","start":"2026-01-01T00:00:00Z","end":"2026-01-01T01:00:00Z","warning":null}}\n' "$rows" "$count"
    ;;
  'auth logout')
    [[ $SIG_STUB_SCENARIO != cleanup ]] || exit 1
    printf '%s\n' '{"data":{"credential_removed":true,"server_key_revoked":false}}'
    ;;
  *) exit 98 ;;
esac
`
