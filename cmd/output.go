package cmd

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/sprisa/sig/config"
	"github.com/sprisa/sig/signoz"
)

var exitDefinitions = []struct {
	Code        int
	Description string
	Kinds       []string
}{
	{0, "success", nil},
	{2, "usage", []string{"usage"}},
	{3, "authentication", []string{"authentication"}},
	{4, "permission", []string{"permission"}},
	{5, "network/timeout/cancelled", []string{"network", "timeout", "cancelled"}},
	{6, "API/response/output/internal", nil},
	{7, "configuration/credentials", []string{"config", "credentials"}},
}

func exitDescriptions() map[string]string {
	result := map[string]string{}
	for _, entry := range exitDefinitions {
		result[strconv.Itoa(entry.Code)] = entry.Description
	}
	return result
}

func fail(code, message string) error { return &signoz.Error{Code: code, Message: message} }

func (a *app) report(err error) int {
	var api *signoz.Error
	var storage *config.Error
	switch {
	case errors.As(err, &api):
	case errors.As(err, &storage):
		api = &signoz.Error{Code: storage.Code, Message: storage.Message}
	default:
		api = &signoz.Error{Code: "internal", Message: "command failed"}
	}
	_ = json.NewEncoder(a.ErrOut).Encode(struct {
		Error *signoz.Error `json:"error"`
	}{api})
	for _, entry := range exitDefinitions {
		for _, kind := range entry.Kinds {
			if api.Code == kind {
				return entry.Code
			}
		}
	}
	return 6
}

func (a *app) emit(data, meta any) error {
	result := struct {
		Data any `json:"data"`
		Meta any `json:"meta,omitempty"`
	}{data, meta}
	if err := json.NewEncoder(a.Out).Encode(result); err != nil {
		return fail("output", "could not write JSON output")
	}
	return nil
}
