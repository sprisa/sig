package cmd

import (
	"testing"

	"github.com/urfave/cli/v3"
)

func TestPolicyFollowsCommandPlacement(t *testing.T) {
	leaf := operation(&cli.Command{Name: "renamed"}, commandPolicy{Effect: "read", Authentication: true, Result: "object"})
	description, err := describeCommand(leaf, []string{"new-group", "renamed"})
	if err != nil || description.Policy == nil || description.Policy.Effect != "read" {
		t.Fatalf("policy depended on an external path registry: %v", err)
	}
}
