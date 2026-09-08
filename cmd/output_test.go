package cmd

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"
)

type outputProbe struct {
	bytes.Buffer
	writes, largest int
	failAfter       int
	short           bool
}

func (w *outputProbe) Write(p []byte) (int, error) {
	w.writes++
	w.largest = max(w.largest, len(p))
	if w.failAfter > 0 && w.writes >= w.failAfter {
		if w.short {
			return len(p) - 1, nil
		}
		return 0, errors.New("private writer detail")
	}
	return w.Buffer.Write(p)
}

func TestStreamingOutput(t *testing.T) {
	row := jsontext.Value(`{"text":"<tag>&\n\"\\\u2028\u2603","integer":9007199254740993,"decimal":1.234567890123456789,"exponent":1e+100,"zero":-0,"null":null}`)
	rows := make([]jsontext.Value, 10000)
	for i := range rows {
		rows[i] = row
	}
	var out outputProbe
	a := app{Options{Out: &out}}
	if err := a.emit(rows, map[string]any{"false": false, "zero": 0, "nil": []string(nil)}); err != nil {
		t.Fatal(err)
	}
	if out.writes < 2 || out.largest >= out.Len()/2 {
		t.Fatal("output buffered the entire row collection")
	}
	if !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
		t.Fatal("missing newline")
	}
	var result struct {
		Data []jsontext.Value          `json:"data"`
		Meta map[string]jsontext.Value `json:"meta"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != len(rows) || string(result.Meta["false"]) != "false" || string(result.Meta["zero"]) != "0" || string(result.Meta["nil"]) != "null" {
		t.Fatal("output contract changed")
	}
	for _, number := range []string{"9007199254740993", "1.234567890123456789", "1e+100", "-0"} {
		if !bytes.Contains(result.Data[0], []byte(number)) {
			t.Fatal("numeric representation changed")
		}
	}
	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result.Data[0], &decoded); err != nil || decoded.Text != "<tag>&\n\"\\\u2028\u2603" {
		t.Fatal("escaping changed string contents")
	}
	for _, short := range []bool{false, true} {
		out := outputProbe{failAfter: 2, short: short}
		a.Out = &out
		err := a.emit(rows, nil)
		if err == nil || !strings.Contains(err.Error(), "could not write JSON output") || strings.Contains(err.Error(), "private") {
			t.Fatalf("write failure not sanitized: %v", err)
		}
	}
	var empty bytes.Buffer
	a.Out = &empty
	if err := a.emit(nil, nil); err != nil || empty.String() != "{\"data\":null}\n" {
		t.Fatal("nil data or omitted metadata changed")
	}
}
