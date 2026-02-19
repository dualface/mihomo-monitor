package main

import "testing"

func TestIsExcludedProxy(t *testing.T) {
	cases := []struct {
		name     string
		expected bool
	}{
		{name: "香港 01", expected: true},
		{name: "Hong Kong 02", expected: true},
		{name: "HK-Edge", expected: true},
		{name: "US 01", expected: false},
	}

	for _, tc := range cases {
		if got := isExcludedProxy(tc.name); got != tc.expected {
			t.Fatalf("isExcludedProxy(%q)=%v want %v", tc.name, got, tc.expected)
		}
	}
}

func TestParseGroupDelaysFilterToggle(t *testing.T) {
	payload := map[string]any{
		"delays": map[string]any{
			"香港 01":       10,
			"HK-Edge":     11,
			"Hong Kong 1": 12,
			"US 01":       20,
		},
	}

	filtered := parseGroupDelays(payload, true)
	if len(filtered) != 1 || filtered[0].Name != "US 01" {
		t.Fatalf("unexpected filtered result: %#v", filtered)
	}

	unfiltered := parseGroupDelays(payload, false)
	if len(unfiltered) != 4 {
		t.Fatalf("unexpected unfiltered result length: %d", len(unfiltered))
	}
}

func TestSanitizeName(t *testing.T) {
	if got := sanitizeName("A!@#香港-(01)"); got != "A香港-(01)" {
		t.Fatalf("sanitizeName mismatch: %q", got)
	}
}
