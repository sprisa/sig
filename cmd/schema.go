package cmd

import (
	"context"
	"strings"

	"github.com/urfave/cli/v3"
)

type commandPolicy struct {
	Effect         string `json:"effect"`
	Authentication bool   `json:"authentication_required"`
	QueryLanguage  string `json:"query_language,omitempty"`
	Pagination     string `json:"pagination,omitempty"`
	Result         string `json:"result_type"`
}

// Safety is explicit operation metadata, not inferred from verbs or HTTP methods.
var commandPolicies = map[string]commandPolicy{
	"auth login":             {Effect: "local_write", Authentication: true, Result: "object"},
	"auth status":            {Effect: "read", Authentication: true, Result: "object"},
	"auth logout":            {Effect: "local_write", Result: "object"},
	"config get-contexts":    {Effect: "local_read", Result: "array"},
	"config current-context": {Effect: "local_read", Result: "object"},
	"config use-context":     {Effect: "local_write", Result: "object"},
	"config delete-context":  {Effect: "local_write", Result: "object"},
	"logs search":            {Effect: "read", Authentication: true, QueryLanguage: "signoz_filter", Pagination: "page_token", Result: "array"},
	"traces search":          {Effect: "read", Authentication: true, QueryLanguage: "signoz_filter", Pagination: "page_token", Result: "array"},
	"traces get":             {Effect: "read", Authentication: true, Pagination: "span_window", Result: "object"},
	"logs fields":            {Effect: "read", Authentication: true, Pagination: "bounded_discovery", Result: "object"},
	"logs values":            {Effect: "read", Authentication: true, QueryLanguage: "signoz_filter", Pagination: "bounded_discovery", Result: "object"},
	"traces fields":          {Effect: "read", Authentication: true, Pagination: "bounded_discovery", Result: "object"},
	"traces values":          {Effect: "read", Authentication: true, QueryLanguage: "signoz_filter", Pagination: "bounded_discovery", Result: "object"},
	"metrics fields":         {Effect: "read", Authentication: true, Pagination: "bounded_discovery", Result: "object"},
	"metrics values":         {Effect: "read", Authentication: true, QueryLanguage: "signoz_filter", Pagination: "bounded_discovery", Result: "object"},
	"metrics list":           {Effect: "read", Authentication: true, Pagination: "bounded_listing", Result: "array"},
	"metrics query":          {Effect: "read", Authentication: true, QueryLanguage: "promql", Result: "object"},
	"services list":          {Effect: "read", Authentication: true, Pagination: "bounded_discovery", Result: "object"},
	"query run":              {Effect: "server_defined", Authentication: true, QueryLanguage: "signoz_v5_json", Result: "object"},
	"query preview":          {Effect: "read", Authentication: true, QueryLanguage: "signoz_v5_json", Result: "object"},
	"agent schema":           {Effect: "local_read", Result: "object"},
	"version":                {Effect: "local_read", Result: "object"},
}

type flagSchema struct {
	Names    []string `json:"names"`
	Type     string   `json:"type"`
	Usage    string   `json:"description"`
	Default  any      `json:"default"`
	Required bool     `json:"required"`
}

type commandSchema struct {
	Name      string          `json:"name"`
	Path      string          `json:"path"`
	Usage     string          `json:"description"`
	Details   string          `json:"details,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Flags     []flagSchema    `json:"flags"`
	Policy    *commandPolicy  `json:"policy,omitempty"`
	Commands  []commandSchema `json:"commands,omitempty"`
}

func describeCommand(cmd *cli.Command, path []string) (commandSchema, error) {
	name := strings.Join(path, " ")
	flags, err := describeFlags(cmd.Flags)
	if err != nil {
		return commandSchema{}, err
	}
	result := commandSchema{Name: cmd.Name, Path: strings.TrimSpace("sig " + name), Usage: cmd.Usage, Details: cmd.Description, Arguments: cmd.ArgsUsage, Flags: flags}
	if len(cmd.Commands) == 0 {
		policy, ok := commandPolicies[name]
		if !ok {
			return commandSchema{}, fail("internal", "agent schema encountered a command without explicit safety metadata")
		}
		result.Policy = &policy
	}
	for _, child := range cmd.Commands {
		if child.Hidden {
			continue
		}
		childPath := append(append([]string{}, path...), child.Name)
		description, err := describeCommand(child, childPath)
		if err != nil {
			return commandSchema{}, err
		}
		result.Commands = append(result.Commands, description)
	}
	return result, nil
}

func (a *app) schema(_ context.Context, cmd *cli.Command) error {
	// A fresh command tree contains defaults only, never invocation or environment values.
	root := a.command()
	selected := root
	path := []string{}
	for _, name := range cmd.Args().Slice() {
		var next *cli.Command
		for _, child := range selected.Commands {
			if child.Name == name {
				next = child
				break
			}
		}
		if next == nil {
			return fail("usage", "unknown command path for agent schema")
		}
		path = append(path, name)
		selected = next
	}
	description, err := describeCommand(selected, path)
	if err != nil {
		return err
	}
	globals, err := describeFlags(root.Flags)
	if err != nil {
		return err
	}
	return a.emit(map[string]any{
		"schema_version": "1", "cli_version": buildVersion(), "command": description, "global_flags": globals,
		"environment": []string{"SIGNOZ_URL", "SIGNOZ_API_KEY", "SIG_CONFIG_DIR"},
		"output":      map[string]any{"success": "stdout: {data, meta?}", "failure": "stderr: {error: {code, message, http_status?}}", "help": "plain text", "numeric_precision": "upstream JSON numbers preserved"},
		"exit_codes":  map[string]string{"0": "success", "2": "usage", "3": "authentication", "4": "permission", "5": "network/timeout/cancelled", "6": "API/response/output", "7": "configuration/credentials"},
		"guidance": []string{
			"Use fields and values to discover queryable attributes; metrics list discovers metric names.",
			"traces search returns spans; traces get may be partial. Check hasMore and hasMissingSpans.",
			"Resume search with next_page_token, not next_cursor. Keep the endpoint unchanged. Tokens contain query filters, not credentials.",
			"Pagination freezes bounds but is not a database snapshot; late ingestion or retention can change offset pages.",
			"query run accepts full v5 JSON, including SQL. It is not a read-only sandbox. Preview may contact ClickHouse; inspect each returned valid/error verdict.",
			"Queries never prompt. Inject credentials or authenticate once with auth login; networking is operator-managed.",
		},
	}, nil)
}

func describeFlags(flags []cli.Flag) ([]flagSchema, error) {
	result := []flagSchema{}
	for _, flag := range flags {
		entry := flagSchema{Names: flag.Names()}
		if doc, ok := flag.(cli.DocGenerationFlag); ok {
			entry.Usage = doc.GetUsage()
		}
		if required, ok := flag.(cli.RequiredFlag); ok {
			entry.Required = required.IsRequired()
		}
		switch f := flag.(type) {
		case *cli.StringFlag:
			entry.Type, entry.Default = "string", f.Value
		case *cli.IntFlag:
			entry.Type, entry.Default = "integer", f.Value
		case *cli.BoolFlag:
			entry.Type, entry.Default = "boolean", f.Value
		case *cli.DurationFlag:
			entry.Type, entry.Default = "duration", f.Value.String()
		case *cli.StringSliceFlag:
			entry.Type, entry.Default = "string_array", f.Value
			if f.Value == nil {
				entry.Default = []string{}
			}
		default:
			return nil, fail("internal", "agent schema encountered an undescribed flag type")
		}
		result = append(result, entry)
	}
	return result, nil
}
