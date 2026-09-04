package jsondocument

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	t.Parallel()

	valid := []byte(`{"outer":{"value":1},"items":[true,null]}`)
	if !Valid(valid, len(valid)) {
		t.Fatal("valid bounded document rejected")
	}
	for _, test := range []struct {
		name  string
		data  []byte
		limit int
	}{
		{name: "empty", limit: 1},
		{name: "zero limit", data: valid},
		{name: "over limit", data: valid, limit: len(valid) - 1},
		{name: "invalid UTF-8", data: []byte{0xff}, limit: 1},
		{name: "duplicate", data: []byte(`{"value":1,"value":2}`), limit: 64},
		{name: "nested duplicate", data: []byte(`{"outer":{"value":1,"value":2}}`), limit: 64},
		{name: "trailing", data: []byte(`{} {}`), limit: 64},
		{name: "deep", data: []byte(strings.Repeat(`[`, 70) + strings.Repeat(`]`, 70)), limit: 256},
	} {
		if Valid(test.data, test.limit) {
			t.Fatalf("%s document accepted", test.name)
		}
	}
}

func TestDecodeAndObjectFields(t *testing.T) {
	t.Parallel()

	type document struct {
		Required string `json:"required"`
		Optional int    `json:"optional,omitempty"`
	}
	var decoded document
	if !Decode([]byte(`{"required":"value"}`), &decoded) || decoded.Required != "value" {
		t.Fatalf("Decode() = %#v", decoded)
	}
	if Decode([]byte(`{"required":"value","unknown":true}`), &decoded) ||
		Decode([]byte(`{"required":"value"} {}`), &decoded) {
		t.Fatal("Decode() accepted unknown or trailing content")
	}

	exact, valid := ExactObject(json.RawMessage(`{"first":1,"second":2}`), "first", "second")
	if !valid || len(exact) != 2 {
		t.Fatal("ExactObject() rejected exact fields")
	}
	if _, valid := ExactObject(json.RawMessage(`{"First":1,"second":2}`), "first", "second"); valid {
		t.Fatal("ExactObject() accepted changed field case")
	}
	if _, valid := ObjectFields(
		json.RawMessage(`{"required":1,"optional":2}`), []string{"required"}, "optional",
	); !valid {
		t.Fatal("ObjectFields() rejected optional field")
	}
	if _, valid := ObjectFields(
		json.RawMessage(`{"optional":2}`), []string{"required"}, "optional",
	); valid {
		t.Fatal("ObjectFields() accepted missing required field")
	}
}
