//go:build !windows

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sprisa/sig/signoz"
	"golang.org/x/term"
)

func TestTerminalKey(t *testing.T) {
	if _, err := exec.LookPath("expect"); err != nil {
		t.Skip("PTY regression tests require expect")
	}
	script := filepath.Join(t.TempDir(), "terminal.exp")
	if err := os.WriteFile(script, []byte(terminalScript), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"success", "ctrl-c", "ctrl-d", "sigint", "sigterm"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "expect", script, os.Args[0], mode)
			command.Env = append(os.Environ(), "SIG_TERMINAL_TEST="+mode)
			out, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("terminal test failed: %v\n%s", err, out)
			}
		})
	}
}

func TestTerminalHelper(t *testing.T) {
	mode := os.Getenv("SIG_TERMINAL_TEST")
	if mode == "" {
		return
	}
	before, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	key, err := readTerminalKey(ctx, os.Stdin, os.Stdout)
	if mode == "success" {
		if err != nil || key != "synthetic-api-key" {
			t.Fatalf("password input failed: %v", err)
		}
	} else {
		var e *signoz.Error
		if !errors.As(err, &e) || e.Code != "cancelled" || key != "" {
			t.Fatal("interruption did not discard the key and return cancellation")
		}
	}
	after, restoreErr := term.GetState(int(os.Stdin.Fd()))
	if restoreErr != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal settings were not restored: before=%#v after=%#v err=%v", before, after, restoreErr)
	}
	fmt.Println("\nTERMINAL_RESTORED")
}

const terminalScript = `
log_user 0
set timeout 5
set mode [lindex $argv 1]
spawn -noecho [lindex $argv 0] -test.run=^TestTerminalHelper$
expect {
  "SigNoz service-account key: " {}
  timeout {exec kill -KILL [exp_pid]; wait; puts "prompt timeout"; exit 1}
  eof {puts "exited before prompt"; exit 1}
}
send -- "synthetic-api-key"
switch -- $mode {
  success {send -- "\r"}
  ctrl-c {send -- "\003"}
  ctrl-d {send -- "\025\004"}
  sigint {exec kill -INT [exp_pid]}
  sigterm {exec kill -TERM [exp_pid]}
}
expect {
  "TERMINAL_RESTORED" {
    if {[string first "synthetic-api-key" $expect_out(buffer)] >= 0} {
      puts "password was echoed"; exit 1
    }
  }
  timeout {exec kill -KILL [exp_pid]; wait; puts "terminal input hung"; exit 1}
  eof {puts "terminal assertion failed: $expect_out(buffer)"; exit 1}
}
expect eof
set result [wait]
exit [lindex $result 3]
`

func TestTerminalKeyAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readTerminalKey(ctx, os.Stdin, &strings.Builder{})
	var e *signoz.Error
	if !errors.As(err, &e) || e.Code != "cancelled" {
		t.Fatal("cancelled context should not initialize a terminal")
	}
}
