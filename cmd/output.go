package cmd

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
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
	_ = json.MarshalEncode(jsontext.NewEncoder(outputWriter{a.ErrOut}), struct {
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
		Meta any `json:"meta,omitzero"`
	}{data, meta}
	if err := json.MarshalEncode(jsontext.NewEncoder(outputWriter{a.Out}), result, json.FormatNilSliceAsNull(true), json.FormatNilMapAsNull(true)); err != nil {
		return fail("output", "could not write JSON output")
	}
	return nil
}

// jsontext does not report a writer's short write with a nil error.
type outputWriter struct{ io.Writer }

func (w outputWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if n < len(p) && err == nil {
		err = io.ErrShortWrite
	}
	return n, err
}
