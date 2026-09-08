package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sprisa/sig/signoz"
)

var resourceBinary = flag.String("resource-binary", "", "compiled sig binary for opt-in process resource measurements")
var resourceCount = flag.Int("resource-count", 3, "number of independent CLI processes per resource scenario")

func TestProcessResources(t *testing.T) {
	if *resourceBinary == "" {
		t.Skip("opt in with task perf:resources")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process measurements require macOS or GNU /usr/bin/time")
	}
	if *resourceCount < 1 || *resourceCount > 20 {
		t.Fatal("resource-count must be 1-20")
	}
	binary, err := filepath.Abs(*resourceBinary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatal("build the requested binary first")
	}
	if _, err := os.Stat("/usr/bin/time"); err != nil {
		t.Fatal("/usr/bin/time is required")
	}
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			server, total := fixtureServer(t, s)
			for attempt := range *resourceCount {
				args := []string{"-l"}
				if runtime.GOOS == "linux" {
					args = []string{"-v"}
				}
				args = append(args, binary, "logs", "search", "--limit", strconv.Itoa(s.rows), "--pages", strconv.Itoa(s.pages), "--start", "2026-01-01T00:00:00Z", "--end", "2026-01-01T00:01:00Z")
				ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
				command := exec.CommandContext(ctx, "/usr/bin/time", args...)
				for _, env := range os.Environ() {
					name, _, _ := strings.Cut(env, "=")
					if strings.HasPrefix(name, "SIGNOZ_") || strings.HasPrefix(name, "SIG_") || name == "LC_ALL" {
						continue
					}
					command.Env = append(command.Env, env)
				}
				command.Env = append(command.Env, "SIGNOZ_URL="+server.URL, "SIGNOZ_API_KEY=synthetic-key", "SIG_CONFIG_DIR="+filepath.Join(t.TempDir(), "config"), "LC_ALL=C")
				command.Stdout = io.Discard
				var stderr bytes.Buffer
				command.Stderr = &stderr
				err := command.Run()
				cancel()
				if err != nil {
					t.Fatalf("resource command failed: %v\n%s", err, stderr.String())
				}
				t.Logf("trial=%d response_bytes=%d", attempt+1, total)
				for _, line := range strings.Split(stderr.String(), "\n") {
					lower := strings.ToLower(line)
					if strings.Contains(lower, " real ") || strings.Contains(lower, "maximum resident set size") || strings.Contains(lower, "peak memory footprint") || strings.Contains(lower, "user time") || strings.Contains(lower, "system time") || strings.Contains(lower, "elapsed (wall clock)") {
						t.Log(strings.TrimSpace(line))
					}
				}
			}
		})
	}
}

func TestRetainedHeap(t *testing.T) {
	if *resourceBinary == "" {
		t.Skip("opt in with task perf:resources")
	}
	for _, s := range scenarios {
		if s.pages != 1 {
			continue
		}
		t.Run(s.name, func(t *testing.T) {
			server, _ := fixtureServer(t, s)
			client, err := signoz.New(server.URL, "synthetic-key", 30*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			request := searchRequest(s.rows)
			if _, err := client.Search(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(map[string]any{
				"start": request.Start.UnixMilli(), "end": request.End.UnixMilli(), "requestType": "raw",
				"compositeQuery": map[string]any{"queries": []any{map[string]any{"type": "builder_query", "spec": map[string]any{"name": "A", "signal": "logs", "limit": s.rows, "offset": 0}}}},
			})
			query, err := signoz.ParseQuery(payload)
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"search_rows", "native_raw_and_results"} {
				runtime.GC()
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				var held any
				if mode == "search_rows" {
					held, err = client.Search(context.Background(), request)
				} else {
					held, err = client.Query(context.Background(), query)
				}
				if err != nil {
					t.Fatal(err)
				}
				runtime.GC()
				runtime.GC()
				runtime.ReadMemStats(&after)
				t.Logf("%s retained_heap_delta_bytes=%d", mode, int64(after.HeapAlloc)-int64(before.HeapAlloc))
				runtime.KeepAlive(held)
			}
		})
	}
}
