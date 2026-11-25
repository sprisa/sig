package cmd

import (
	"runtime"
	"strings"
	"testing"
)

func keyCommandHarness(t *testing.T) *harness {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("key command tests use a POSIX shell")
	}
	h := newHarness(t)
	h.env["SIGNOZ_URL"] = identityServer(t).URL
	return h
}

func TestKeyCommandSuppliesTheKey(t *testing.T) {
	h := keyCommandHarness(t)
	h.env["SIGNOZ_API_KEY_COMMAND"] = "printf 'synthetic-api-key\\n'"
	got := h.run(t, "", 0, "auth", "status")
	if !strings.Contains(got, `"credential_source":"command"`) || !strings.Contains(got, `"id":"account-1"`) {
		t.Fatal(got)
	}
	if strings.Contains(got, "synthetic-api-key") {
		t.Fatal("status leaked key")
	}
	if len(h.keys.keys) != 0 {
		t.Fatal("command key was stored")
	}
}

func TestEnvironmentKeyWinsOverKeyCommand(t *testing.T) {
	h := keyCommandHarness(t)
	h.env["SIGNOZ_API_KEY"] = "synthetic-api-key"
	h.env["SIGNOZ_API_KEY_COMMAND"] = "exit 1"
	got := h.run(t, "", 0, "auth", "status")
	if !strings.Contains(got, `"credential_source":"environment"`) {
		t.Fatal(got)
	}
}

func TestKeyCommandWorksWithAnUnknownContext(t *testing.T) {
	h := keyCommandHarness(t)
	h.env["SIGNOZ_API_KEY_COMMAND"] = "echo synthetic-api-key"
	h.run(t, "", 0, "--context", "ephemeral", "auth", "status")
}

func TestKeyCommandFailuresHideHelperOutput(t *testing.T) {
	for name, command := range map[string]string{
		"exit status": "echo synthetic-api-key; echo helper-diagnostic >&2; exit 3",
		"empty":       "printf '  \\n'",
		"too long":    "head -c 70000 /dev/zero | tr '\\0' k",
	} {
		t.Run(name, func(t *testing.T) {
			h := keyCommandHarness(t)
			h.env["SIGNOZ_API_KEY_COMMAND"] = command
			got := h.run(t, "", 3, "auth", "status")
			if !strings.Contains(got, `"code":"authentication"`) || strings.Contains(got, "helper-diagnostic") {
				t.Fatal(got)
			}
		})
	}
}

func TestKeyCommandTimeout(t *testing.T) {
	h := keyCommandHarness(t)
	h.env["SIGNOZ_API_KEY_COMMAND"] = "sleep 5"
	got := h.run(t, "", 5, "--timeout", "100ms", "auth", "status")
	if !strings.Contains(got, `"code":"timeout"`) {
		t.Fatal(got)
	}
}
