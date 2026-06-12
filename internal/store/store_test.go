package store

import "testing"

func TestJSONEqualIgnoresObjectKeyOrder(t *testing.T) {
	left := []byte(`{"a":1,"b":{"c":true}}`)
	right := []byte(`{"b":{"c":true},"a":1}`)

	if !jsonEqual(left, right) {
		t.Fatal("expected equivalent JSON objects")
	}
}

func TestJSONEqualRejectsDifferentObjects(t *testing.T) {
	left := []byte(`{"a":1}`)
	right := []byte(`{"a":2}`)

	if jsonEqual(left, right) {
		t.Fatal("expected different JSON objects")
	}
}

func TestAddInt64DetectsOverflow(t *testing.T) {
	if _, ok := addInt64(9223372036854775807, 1); ok {
		t.Fatal("expected overflow")
	}
}
