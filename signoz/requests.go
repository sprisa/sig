package signoz

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type Signal string

const (
	Logs            Signal = "logs"
	Traces          Signal = "traces"
	Metrics         Signal = "metrics"
	MaxQueryBytes          = 1 << 20
	MaxSearchRows          = 10000
	MaxSearchOffset        = 1000000
)

func (s Signal) Validate() error {
	if s != Logs && s != Traces && s != Metrics {
		return usage("signal must be logs, traces, or metrics")
	}
	return nil
}

type Window struct{ Start, End time.Time }

func (w Window) Validate() error {
	if w.Start.UnixMilli() <= 0 || w.Start.UnixMilli() >= w.End.UnixMilli() || w.Start.Year() > 9999 || w.End.Year() > 9999 {
		return usage("start/end must be positive millisecond timestamps within RFC3339's year range, with start before end")
	}
	return nil
}

func usage(message string) error { return &Error{Code: "usage", Message: message} }

type SearchRequest struct {
	Window
	Signal        Signal
	Where         string
	Limit, Offset int
}

func (r SearchRequest) Validate() error {
	if err := r.Window.Validate(); err != nil {
		return err
	}
	if r.Signal != Logs && r.Signal != Traces {
		return usage("search signal must be logs or traces")
	}
	if r.Limit < 1 || r.Limit > MaxSearchRows || r.Offset < 0 || r.Offset > MaxSearchOffset {
		return usage("search limit must be 1-10000 and offset 0-1000000")
	}
	return nil
}

type MetricsQueryRequest struct {
	Window
	Expression string
	Step       time.Duration
	NoCache    bool
}

func (r MetricsQueryRequest) Validate() error {
	if err := r.Window.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Expression) == "" || r.Step < time.Second || r.Step%time.Second != 0 {
		return usage("PromQL is required and step must be positive whole seconds")
	}
	if (r.End.UnixMilli()-r.Start.UnixMilli())/r.Step.Milliseconds()+1 > 11000 {
		return usage("query exceeds 11000 points per series; increase step or narrow the time window")
	}
	return nil
}

type MetricsListRequest struct {
	Window
	Limit  int
	Search string
}

func (r MetricsListRequest) Validate() error {
	if err := r.Window.Validate(); err != nil {
		return err
	}
	if r.Limit < 1 || r.Limit > 5000 {
		return usage("metric list limit must be between 1 and 5000")
	}
	return nil
}

type TraceRequest struct {
	ID, SelectedSpan string
	ExpandedSpans    []string
}

func validID(id string, size int) bool {
	if len(id) != size || strings.Trim(id, "0") == "" {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (r TraceRequest) Validate() error {
	if !validID(r.ID, 32) || (r.SelectedSpan != "" && !validID(r.SelectedSpan, 16)) {
		return usage("trace ID must be nonzero 32-character hex; span IDs must be nonzero 16-character hex")
	}
	for _, id := range r.ExpandedSpans {
		if !validID(id, 16) {
			return usage("expanded span IDs must be nonzero 16-character hex")
		}
	}
	return nil
}

type FieldKeysRequest struct {
	Window
	Signal                                     Signal
	Limit                                      int
	Search, FieldContext, DataType, MetricName string
}

func (r FieldKeysRequest) Validate() error {
	if err := r.Window.Validate(); err != nil {
		return err
	}
	if err := r.Signal.Validate(); err != nil {
		return err
	}
	if r.Limit < 1 || r.Limit > 1000 {
		return usage("discovery limit must be between 1 and 1000")
	}
	return nil
}

type FieldValuesRequest struct {
	FieldKeysRequest
	Name, Where string
}

func (r FieldValuesRequest) Validate() error {
	if err := r.FieldKeysRequest.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Name) == "" {
		return usage("value discovery requires a field name")
	}
	return nil
}

// NativeQuery is validated once on input and retains the original JSON numbers
// and open-ended expressions. Its contents cannot be mutated by callers.
type NativeQuery struct {
	payload json.RawMessage
	kind    string
}

func ParseQuery(payload []byte) (NativeQuery, error) {
	if len(payload) > MaxQueryBytes {
		return NativeQuery{}, usage("query file exceeds 1 MiB")
	}
	var r struct {
		Start, End     int64
		RequestType    string
		CompositeQuery struct{ Queries []json.RawMessage }
	}
	if json.Unmarshal(payload, &r) != nil || len(r.CompositeQuery.Queries) == 0 {
		return NativeQuery{}, usage("query must be one v5 JSON object with millisecond start/end and compositeQuery.queries")
	}
	if err := (Window{Start: time.UnixMilli(r.Start), End: time.UnixMilli(r.End)}).Validate(); err != nil {
		return NativeQuery{}, err
	}
	switch r.RequestType {
	case "raw", "scalar", "time_series", "trace":
	default:
		return NativeQuery{}, usage("query requestType must be raw, scalar, time_series, or trace")
	}
	return NativeQuery{payload: append(json.RawMessage(nil), payload...), kind: r.RequestType}, nil
}
