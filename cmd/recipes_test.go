package cmd

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

func TestOfflineRecipes(t *testing.T) {
	h := newHarness(t)
	// Deliberately unusable connection settings must not affect bundled recipes.
	h.env["SIGNOZ_URL"] = "not-a-url"
	catalog := h.run(t, "", 0, "agent", "recipes")
	var listing struct {
		Data []struct {
			Topic   string `json:"topic"`
			Summary string `json:"summary"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(catalog), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Data) != 5 {
		t.Fatal("missing recipe topics")
	}
	for _, entry := range listing.Data {
		got := h.run(t, "", 0, "agent", "recipes", entry.Topic)
		var result struct {
			Data struct {
				Topic   string `json:"topic"`
				Content string `json:"content"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(got), &result); err != nil {
			t.Fatal(err)
		}
		if result.Data.Topic != entry.Topic || !strings.HasPrefix(result.Data.Content, "# ") || entry.Summary == "" {
			t.Fatal("recipe content missing")
		}
		if entry.Topic == "workflow" {
			for _, contract := range []string{
				"| `logs search`, `traces search` | `meta.warning`, `meta.warnings`",
				"| `logs aggregate`, `traces aggregate` | `data.warning`",
				"| `metrics query`, `query run` | `data.warning`",
			} {
				if !strings.Contains(result.Data.Content, contract) {
					t.Fatal("workflow recipe lost command-specific warning guidance")
				}
			}
		}
	}
	h.run(t, "", 2, "agent", "recipes", "unknown")
	h.run(t, "", 2, "agent", "recipes", "../workflow")
	h.run(t, "", 2, "agent", "recipes", "logs", "traces")
	schema := h.run(t, "", 0, "agent", "schema", "agent", "recipes")
	if !strings.Contains(schema, `"effect":"local_read"`) || !strings.Contains(schema, `"authentication_required":false`) {
		t.Fatal("recipe schema must advertise offline operation")
	}
}
