package signoz

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/url"
	"strconv"
)

func discoveryParams(r FieldKeysRequest) url.Values {
	p := url.Values{"signal": {string(r.Signal)}, "startUnixMilli": {strconv.FormatInt(r.Start.UnixMilli(), 10)}, "endUnixMilli": {strconv.FormatInt(r.End.UnixMilli(), 10)}, "limit": {strconv.Itoa(r.Limit)}}
	for key, value := range map[string]string{"searchText": r.Search, "fieldContext": r.FieldContext, "fieldDataType": r.DataType, "metricName": r.MetricName} {
		if value != "" {
			p.Set(key, value)
		}
	}
	return p
}

func (c *Client) FieldKeys(ctx context.Context, r FieldKeysRequest) (FieldKeysResult, error) {
	if err := r.Validate(); err != nil {
		return FieldKeysResult{}, err
	}
	data, err := request[jsontext.Value](ctx, c, http.MethodGet, "/api/v1/fields/keys", nil, discoveryParams(r))
	if err != nil {
		return FieldKeysResult{}, err
	}
	var wire struct {
		Keys     jsontext.Value `json:"keys"`
		Complete *bool          `json:"complete"`
	}
	var keys map[string][]FieldKey
	if json.Unmarshal(data, &wire) != nil || wire.Complete == nil || json.Unmarshal(wire.Keys, &keys) != nil {
		return FieldKeysResult{}, invalidResponse("unexpected field-key discovery response")
	}
	return FieldKeysResult{Raw: data, Keys: keys, Complete: *wire.Complete}, nil
}

func (c *Client) FieldValues(ctx context.Context, r FieldValuesRequest) (FieldValuesResult, error) {
	if err := r.Validate(); err != nil {
		return FieldValuesResult{}, err
	}
	p := discoveryParams(r.FieldKeysRequest)
	p.Set("name", r.Name)
	if r.Where != "" {
		p.Set("existingQuery", r.Where)
	}
	data, err := request[jsontext.Value](ctx, c, http.MethodGet, "/api/v1/fields/values", nil, p)
	if err != nil {
		return FieldValuesResult{}, err
	}
	var wire struct {
		Values   jsontext.Value `json:"values"`
		Complete *bool          `json:"complete"`
	}
	var values FieldValues
	if json.Unmarshal(data, &wire) != nil || wire.Complete == nil || json.Unmarshal(wire.Values, &values) != nil {
		return FieldValuesResult{}, invalidResponse("unexpected field-value discovery response")
	}
	return FieldValuesResult{Raw: data, Values: values, Complete: *wire.Complete}, nil
}
