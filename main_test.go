package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

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

func TestControllerRequestNoContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cfg := Config{ControllerURL: server.URL}
	payload, err := controllerRequest(server.Client(), cfg, http.MethodPut, server.URL, []byte(`{"name":"x"}`))
	if err != nil {
		t.Fatalf("controllerRequest returned unexpected error: %v", err)
	}
	if len(payload) != 0 {
		t.Fatalf("expected empty payload, got %#v", payload)
	}
}

func TestFindBestAlternative(t *testing.T) {
	delays := []ProxyDelay{
		{Name: "A", DelayMS: 10},
		{Name: "B", DelayMS: 20},
		{Name: "C", DelayMS: 30},
	}

	got, ok := findBestAlternative(delays, "A")
	if !ok {
		t.Fatalf("expected alternative, got none")
	}
	if got.Name != "B" {
		t.Fatalf("expected B, got %s", got.Name)
	}

	_, ok = findBestAlternative([]ProxyDelay{{Name: "A", DelayMS: 10}}, "A")
	if ok {
		t.Fatalf("expected no alternative, but got one")
	}
}

func TestLoadConfigRejectsInvalidThresholds(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	t.Setenv("MIHOMO_CONTROLLER_URL", "http://127.0.0.1:51002")

	t.Setenv("DELAY_TIMEOUT_MS", "0")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "DELAY_TIMEOUT_MS") {
		t.Fatalf("expected DELAY_TIMEOUT_MS validation error, got %v", err)
	}

	t.Setenv("DELAY_TIMEOUT_MS", "3000")
	t.Setenv("AUTO_SELECT_DIFF_MS", "-1")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "AUTO_SELECT_DIFF_MS") {
		t.Fatalf("expected AUTO_SELECT_DIFF_MS validation error, got %v", err)
	}

	t.Setenv("AUTO_SELECT_DIFF_MS", "300")
	t.Setenv("MONITOR_INTERVAL_S", "0")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "MONITOR_INTERVAL_S") {
		t.Fatalf("expected MONITOR_INTERVAL_S validation error, got %v", err)
	}

	t.Setenv("MONITOR_INTERVAL_S", "300")
	t.Setenv("KEEP_DELAY_THRESHOLD_MS", "-1")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "KEEP_DELAY_THRESHOLD_MS") {
		t.Fatalf("expected KEEP_DELAY_THRESHOLD_MS validation error, got %v", err)
	}
}

func TestFindBestReachableAlternative(t *testing.T) {
	delayMap := map[string]int{
		"A|https://e1.example": 20,
		"A|https://e2.example": -1,
		"B|https://e1.example": 30,
		"B|https://e2.example": 35,
		"C|https://e1.example": 25,
		"C|https://e2.example": 28,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 3 || parts[0] != "proxies" || parts[2] != "delay" {
			http.NotFound(w, r)
			return
		}
		key := parts[1] + "|" + r.URL.Query().Get("url")
		delay, ok := delayMap[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"delay": delay})
	}))
	defer server.Close()

	cfg := Config{
		ControllerURL:  server.URL,
		DelayTimeoutMS: 3000,
		EndpointURLs:   []string{"https://e1.example", "https://e2.example"},
	}
	delays := []ProxyDelay{
		{Name: "A", DelayMS: 10},
		{Name: "C", DelayMS: 12},
		{Name: "B", DelayMS: 15},
	}

	got, ok := findBestReachableAlternative(server.Client(), cfg, delays, "CURRENT", cfg.EndpointURLs)
	if !ok {
		t.Fatalf("expected reachable alternative")
	}
	if got.Name != "C" {
		t.Fatalf("expected C, got %s", got.Name)
	}
}

func TestParseArgsDryRunValidation(t *testing.T) {
	args, err := parseArgsFrom([]string{"--auto-select", "--dry-run", "--json"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !args.AutoSelect || !args.DryRun || !args.JSONOutput {
		t.Fatalf("unexpected parsed args: %+v", args)
	}

	_, err = parseArgsFrom([]string{"--print-current", "--dry-run"})
	if err == nil || !strings.Contains(err.Error(), "--dry-run can only be used") {
		t.Fatalf("expected dry-run validation error, got %v", err)
	}
}

func TestAutoSelectDryRunDoesNotSwitch(t *testing.T) {
	var putCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/proxies/PROXY":
			_ = json.NewEncoder(w).Encode(map[string]any{"now": "A"})
		case r.Method == http.MethodGet && r.URL.Path == "/group/PROXY/delay":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"delays": map[string]any{
					"A": 500,
					"B": 100,
				},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/proxies/PROXY":
			atomic.AddInt32(&putCalls, 1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := Config{
		ControllerURL:        server.URL,
		ProxyGroup:           "PROXY",
		TestURL:              "https://example.com",
		DelayTimeoutMS:       3000,
		AutoSelectDiffMS:     100,
		KeepDelayThresholdMS: 200,
		FilterHKNodes:        false,
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe create failed: %v", err)
	}
	os.Stdout = w
	autoSelectOnce(server.Client(), cfg, true, true)
	_ = w.Close()
	os.Stdout = oldStdout

	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout failed: %v", err)
	}
	_ = r.Close()

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("json unmarshal failed: %v, raw=%q", err, string(raw))
	}
	if payload["action"] != "would_switch" {
		t.Fatalf("expected action would_switch, got %#v", payload["action"])
	}
	if payload["dry_run"] != true {
		t.Fatalf("expected dry_run=true, got %#v", payload["dry_run"])
	}
	if atomic.LoadInt32(&putCalls) != 0 {
		t.Fatalf("expected no PUT calls in dry-run, got %d", putCalls)
	}
}
