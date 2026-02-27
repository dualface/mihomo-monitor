package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/joho/godotenv"
	"golang.org/x/net/proxy"
)

type Config struct {
	ControllerURL        string
	ControllerSecret     string
	ProxyGroup           string
	TestURL              string
	DelayTimeoutMS       int
	AutoSelectDiffMS     int
	MonitorIntervalS     int
	EndpointURLs         []string
	KeepDelayThresholdMS int
	ProxyAddr            string
	FilterHKNodes        bool
}

type ProxyDelay struct {
	Name    string
	DelayMS int
}

type EndpointResult struct {
	URL       string `json:"url"`
	Reachable bool   `json:"reachable"`
	LatencyMS int    `json:"latency_ms"`
}

var hkTokenRE = regexp.MustCompile(`(?i)(^|[^a-z0-9])hk([^a-z0-9]|$)`)

const endpointProbeCandidateLimit = 10

func isExcludedProxy(name string) bool {
	lowered := strings.ToLower(name)
	if strings.Contains(name, "香港") {
		return true
	}
	if strings.Contains(lowered, "hong kong") {
		return true
	}
	return hkTokenRE.MatchString(lowered)
}

func parseBoolEnv(name string, defaultVal bool) bool {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return defaultVal
	}
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		log.Printf("Invalid %s=%q, fallback to %v", name, raw, defaultVal)
		return defaultVal
	}
}

func envOrDefault(name, defaultVal string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return defaultVal
	}
	return v
}

func parseIntEnv(name string, defaultVal int) (int, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return defaultVal, nil
	}
	parsed, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func loadConfig() (Config, error) {
	_ = godotenv.Overload()

	controllerURL := strings.TrimSpace(os.Getenv("MIHOMO_CONTROLLER_URL"))
	if controllerURL == "" {
		return Config{}, errors.New("MIHOMO_CONTROLLER_URL is required")
	}

	rawEndpoints := strings.TrimSpace(os.Getenv("ENDPOINT_URLS"))
	endpointURLs := make([]string, 0)
	if rawEndpoints != "" {
		for _, item := range strings.Split(rawEndpoints, ",") {
			trimmed := strings.TrimSpace(item)
			if trimmed != "" {
				endpointURLs = append(endpointURLs, trimmed)
			}
		}
	}

	delayTimeoutMS, err := parseIntEnv("DELAY_TIMEOUT_MS", 3000)
	if err != nil {
		return Config{}, err
	}
	if delayTimeoutMS <= 0 {
		return Config{}, errors.New("DELAY_TIMEOUT_MS must be > 0")
	}
	autoSelectDiffMS, err := parseIntEnv("AUTO_SELECT_DIFF_MS", 300)
	if err != nil {
		return Config{}, err
	}
	if autoSelectDiffMS < 0 {
		return Config{}, errors.New("AUTO_SELECT_DIFF_MS must be >= 0")
	}
	monitorIntervalS, err := parseIntEnv("MONITOR_INTERVAL_S", 300)
	if err != nil {
		return Config{}, err
	}
	if monitorIntervalS <= 0 {
		return Config{}, errors.New("MONITOR_INTERVAL_S must be > 0")
	}
	keepDelayThresholdMS, err := parseIntEnv("KEEP_DELAY_THRESHOLD_MS", 2000)
	if err != nil {
		return Config{}, err
	}
	if keepDelayThresholdMS < 0 {
		return Config{}, errors.New("KEEP_DELAY_THRESHOLD_MS must be >= 0")
	}

	proxyAddr := strings.TrimSpace(os.Getenv("MIHOMO_PROXY_ADDR"))
	if len(endpointURLs) > 0 && proxyAddr == "" {
		log.Printf("Warning: ENDPOINT_URLS is set but MIHOMO_PROXY_ADDR is empty; endpoint checks are disabled")
	}

	return Config{
		ControllerURL:        strings.TrimRight(controllerURL, "/"),
		ControllerSecret:     strings.TrimSpace(os.Getenv("MIHOMO_CONTROLLER_SECRET")),
		ProxyGroup:           envOrDefault("MIHOMO_PROXY_GROUP", "GLOBAL"),
		TestURL:              envOrDefault("TEST_URL", "https://google.com"),
		DelayTimeoutMS:       delayTimeoutMS,
		AutoSelectDiffMS:     autoSelectDiffMS,
		MonitorIntervalS:     monitorIntervalS,
		EndpointURLs:         endpointURLs,
		KeepDelayThresholdMS: keepDelayThresholdMS,
		ProxyAddr:            proxyAddr,
		FilterHKNodes:        parseBoolEnv("FILTER_HK_NODES", true),
	}, nil
}

func setAuthHeader(req *http.Request, secret string) {
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
}

func toInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	case string:
		i, err := strconv.Atoi(v)
		if err != nil {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}

func parseGroupDelays(payload map[string]any, filterHKNodes bool) []ProxyDelay {
	delays := make([]ProxyDelay, 0)

	if delaysRaw, ok := payload["delays"].(map[string]any); ok {
		for name, delay := range delaysRaw {
			if filterHKNodes && isExcludedProxy(name) {
				continue
			}
			delayMS, ok := toInt(delay)
			if !ok {
				continue
			}
			if delayMS >= 0 {
				delays = append(delays, ProxyDelay{Name: name, DelayMS: delayMS})
			}
		}
		return delays
	}

	for name, delay := range payload {
		if filterHKNodes && isExcludedProxy(name) {
			continue
		}
		delayMS, ok := toInt(delay)
		if !ok {
			continue
		}
		if delayMS >= 0 {
			delays = append(delays, ProxyDelay{Name: name, DelayMS: delayMS})
		}
	}
	if len(delays) > 0 {
		return delays
	}

	if proxiesRaw, ok := payload["proxies"].([]any); ok {
		for _, item := range proxiesRaw {
			proxyItem, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, ok := proxyItem["name"].(string)
			if !ok {
				continue
			}
			if filterHKNodes && isExcludedProxy(name) {
				continue
			}
			delayMS, ok := toInt(proxyItem["delay"])
			if !ok {
				continue
			}
			if delayMS >= 0 {
				delays = append(delays, ProxyDelay{Name: name, DelayMS: delayMS})
			}
		}
		return delays
	}

	name, hasName := payload["name"].(string)
	delay, hasDelay := payload["delay"]
	if hasName && hasDelay {
		if filterHKNodes && isExcludedProxy(name) {
			return []ProxyDelay{}
		}
		delayMS, ok := toInt(delay)
		if ok && delayMS >= 0 {
			return []ProxyDelay{{Name: name, DelayMS: delayMS}}
		}
	}

	log.Printf("Unexpected delay payload shape: %v", payload)
	return []ProxyDelay{}
}

func controllerRequest(client *http.Client, cfg Config, method, endpoint string, body []byte) (map[string]any, error) {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader([]byte{})
	} else {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	setAuthHeader(req, cfg.ControllerSecret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("request failed: %s", resp.Status)
	}
	if resp.StatusCode == http.StatusNoContent || resp.ContentLength == 0 {
		return map[string]any{}, nil
	}
	var payload map[string]any
	decoder := json.NewDecoder(resp.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		if errors.Is(err, io.EOF) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	return payload, nil
}

func getGroupDelaysWithFilter(client *http.Client, cfg Config, filterHKNodes bool) []ProxyDelay {
	endpoint := fmt.Sprintf("%s/group/%s/delay", cfg.ControllerURL, url.PathEscape(cfg.ProxyGroup))
	params := url.Values{}
	params.Set("url", cfg.TestURL)
	params.Set("timeout", strconv.Itoa(cfg.DelayTimeoutMS))
	endpoint = endpoint + "?" + params.Encode()

	payload, err := controllerRequest(client, cfg, http.MethodGet, endpoint, nil)
	if err != nil {
		log.Printf("Group delay check failed: %v", err)
		return []ProxyDelay{}
	}
	return parseGroupDelays(payload, filterHKNodes)
}

func getGroupDelays(client *http.Client, cfg Config) []ProxyDelay {
	return getGroupDelaysWithFilter(client, cfg, cfg.FilterHKNodes)
}

func findBestAlternative(delays []ProxyDelay, current string) (ProxyDelay, bool) {
	for _, item := range delays {
		if item.Name != current {
			return item, true
		}
	}
	return ProxyDelay{}, false
}

func getProxyDelay(client *http.Client, cfg Config, proxyName, targetURL string, timeoutMS int) (int, bool) {
	endpoint := fmt.Sprintf("%s/proxies/%s/delay", cfg.ControllerURL, url.PathEscape(proxyName))
	params := url.Values{}
	params.Set("url", targetURL)
	params.Set("timeout", strconv.Itoa(timeoutMS))
	endpoint = endpoint + "?" + params.Encode()

	payload, err := controllerRequest(client, cfg, http.MethodGet, endpoint, nil)
	if err != nil {
		return -1, false
	}
	delayRaw, ok := payload["delay"]
	if !ok {
		return -1, false
	}
	delayMS, ok := toInt(delayRaw)
	if !ok || delayMS < 0 {
		return -1, false
	}
	return delayMS, true
}

func isProxyReachableForEndpoints(client *http.Client, cfg Config, proxyName string, endpointURLs []string) bool {
	if len(endpointURLs) == 0 {
		return true
	}
	for _, target := range endpointURLs {
		if _, ok := getProxyDelay(client, cfg, proxyName, target, cfg.DelayTimeoutMS); !ok {
			return false
		}
	}
	return true
}

func findBestReachableAlternative(client *http.Client, cfg Config, delays []ProxyDelay, current string, endpointURLs []string) (ProxyDelay, bool) {
	if len(endpointURLs) == 0 {
		return findBestAlternative(delays, current)
	}
	checked := 0
	for _, item := range delays {
		if item.Name == current {
			continue
		}
		if checked >= endpointProbeCandidateLimit {
			break
		}
		checked++
		if isProxyReachableForEndpoints(client, cfg, item.Name, endpointURLs) {
			return item, true
		}
	}
	return ProxyDelay{}, false
}

func sanitizeName(name string) string {
	const safePunct = " .-_()/[]:"
	var b strings.Builder
	for _, r := range name {
		if strings.ContainsRune(safePunct, r) {
			b.WriteRune(r)
			continue
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func getCurrentProxy(client *http.Client, cfg Config) (string, bool) {
	endpoint := fmt.Sprintf("%s/proxies/%s", cfg.ControllerURL, url.PathEscape(cfg.ProxyGroup))
	payload, err := controllerRequest(client, cfg, http.MethodGet, endpoint, nil)
	if err != nil {
		log.Printf("Current proxy check failed: %v", err)
		return "", false
	}
	now, ok := payload["now"].(string)
	if !ok || now == "" {
		return "", false
	}
	return now, true
}

func switchProxy(client *http.Client, cfg Config, candidate ProxyDelay) error {
	endpoint := fmt.Sprintf("%s/proxies/%s", cfg.ControllerURL, url.PathEscape(cfg.ProxyGroup))
	body, err := json.Marshal(map[string]string{"name": candidate.Name})
	if err != nil {
		return err
	}
	_, err = controllerRequest(client, cfg, http.MethodPut, endpoint, body)
	return err
}

func buildTransportForProxy(proxyAddr string) (*http.Transport, error) {
	transport, err := buildBaseTransportNoEnvProxy()
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(proxyAddr) == "" {
		return transport, nil
	}

	proxyURL, err := url.Parse(proxyAddr)
	if err != nil {
		return nil, err
	}

	scheme := strings.ToLower(proxyURL.Scheme)
	switch scheme {
	case "http", "https":
		transport.Proxy = http.ProxyURL(proxyURL)
		return transport, nil
	case "socks5", "socks5h":
		dialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			return nil, err
		}
		transport.Proxy = nil
		transport.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		}
		return transport, nil
	default:
		return nil, fmt.Errorf("unsupported proxy scheme: %s", scheme)
	}
}

func buildBaseTransportNoEnvProxy() (*http.Transport, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default transport type assertion failed")
	}
	transport := base.Clone()
	transport.Proxy = nil
	return transport, nil
}

func checkEndpoint(proxyAddr, targetURL string, timeout time.Duration) EndpointResult {
	transport, err := buildTransportForProxy(proxyAddr)
	if err != nil {
		return EndpointResult{URL: targetURL, Reachable: false, LatencyMS: -1}
	}
	client := &http.Client{Transport: transport, Timeout: timeout}
	req, err := http.NewRequest(http.MethodHead, targetURL, nil)
	if err != nil {
		return EndpointResult{URL: targetURL, Reachable: false, LatencyMS: -1}
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return EndpointResult{URL: targetURL, Reachable: false, LatencyMS: -1}
	}
	defer resp.Body.Close()

	latencyMS := int(time.Since(start).Milliseconds())
	return EndpointResult{URL: targetURL, Reachable: resp.StatusCode < 500, LatencyMS: latencyMS}
}

func checkAllEndpoints(proxyAddr string, urls []string) []EndpointResult {
	if len(urls) == 0 || strings.TrimSpace(proxyAddr) == "" {
		return []EndpointResult{}
	}
	results := make([]EndpointResult, len(urls))
	var wg sync.WaitGroup
	for idx, endpoint := range urls {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			results[i] = checkEndpoint(proxyAddr, target, 10*time.Second)
		}(idx, endpoint)
	}
	wg.Wait()
	return results
}

func mustASCIIJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return escapeNonASCII(raw)
}

func escapeNonASCII(raw []byte) string {
	buf := make([]byte, 0, len(raw)+16)
	for i := 0; i < len(raw); {
		if raw[i] < utf8.RuneSelf {
			buf = append(buf, raw[i])
			i++
			continue
		}
		r, size := utf8.DecodeRune(raw[i:])
		if r == utf8.RuneError && size == 1 {
			buf = append(buf, raw[i])
			i++
			continue
		}
		buf = appendEscapedRune(buf, r)
		i += size
	}
	return string(buf)
}

func appendEscapedRune(dst []byte, r rune) []byte {
	if r <= 0xFFFF {
		return append(dst, []byte(fmt.Sprintf("\\u%04x", r))...)
	}
	for _, part := range utf16.Encode([]rune{r}) {
		dst = append(dst, []byte(fmt.Sprintf("\\u%04x", part))...)
	}
	return dst
}

func printDelaysOnce(client *http.Client, cfg Config, jsonOutput bool) {
	delays := getGroupDelays(client, cfg)
	sortDelays(delays)
	if len(delays) > 10 {
		delays = delays[:10]
	}

	if len(delays) == 0 {
		if jsonOutput {
			fmt.Println("[]")
		} else {
			fmt.Println("No delay data returned")
		}
		return
	}

	if jsonOutput {
		payload := make([]map[string]any, 0, len(delays))
		for _, item := range delays {
			payload = append(payload, map[string]any{"name": item.Name, "delay_ms": item.DelayMS})
		}
		fmt.Println(mustASCIIJSON(payload))
		return
	}

	for _, item := range delays {
		fmt.Printf("%dms\t%s\n", item.DelayMS, sanitizeName(item.Name))
	}
}

func sortDelays(delays []ProxyDelay) {
	for i := 1; i < len(delays); i++ {
		j := i
		for j > 0 && delays[j-1].DelayMS > delays[j].DelayMS {
			delays[j-1], delays[j] = delays[j], delays[j-1]
			j--
		}
	}
}

func printCurrentDelayOnce(client *http.Client, cfg Config, jsonOutput bool) {
	current, ok := getCurrentProxy(client, cfg)
	if !ok {
		if jsonOutput {
			fmt.Println(mustASCIIJSON(map[string]any{"error": "current proxy not found"}))
		} else {
			fmt.Println("Current proxy not found")
		}
		return
	}

	delays := getGroupDelaysWithFilter(client, cfg, false)
	delayMap := make(map[string]int, len(delays))
	for _, item := range delays {
		delayMap[item.Name] = item.DelayMS
	}

	delayMS, exists := delayMap[current]
	if !exists {
		if jsonOutput {
			fmt.Println(mustASCIIJSON(map[string]any{"name": current, "delay_ms": nil}))
		} else {
			fmt.Printf("delay unavailable\t%s\n", sanitizeName(current))
		}
		return
	}

	if jsonOutput {
		fmt.Println(mustASCIIJSON(map[string]any{"name": current, "delay_ms": delayMS}))
		return
	}
	fmt.Printf("%dms\t%s\n", delayMS, sanitizeName(current))
}

func autoSelectOnce(client *http.Client, cfg Config, jsonOutput, dryRun bool) {
	current, currentFound := getCurrentProxy(client, cfg)
	delays := getGroupDelays(client, cfg)
	sortDelays(delays)
	if len(delays) == 0 && cfg.FilterHKNodes {
		delays = getGroupDelaysWithFilter(client, cfg, false)
		sortDelays(delays)
		if len(delays) > 0 {
			log.Printf("FILTER_HK_NODES removed all delay candidates; fallback to unfiltered delays")
		}
	}

	if len(delays) == 0 {
		if jsonOutput {
			fmt.Println(mustASCIIJSON(map[string]any{"error": "no delay data"}))
		} else {
			fmt.Println("No delay data returned")
		}
		return
	}

	best := delays[0]
	allDelays := getGroupDelaysWithFilter(client, cfg, false)
	delayMap := make(map[string]int, len(allDelays))
	for _, item := range allDelays {
		delayMap[item.Name] = item.DelayMS
	}

	var currentDelay *int
	if currentFound {
		if d, exists := delayMap[current]; exists {
			currentDelay = &d
		}
	}

	endpointResults := []EndpointResult{}
	allEndpointsOK := true
	if len(cfg.EndpointURLs) > 0 && strings.TrimSpace(cfg.ProxyAddr) != "" {
		endpointResults = checkAllEndpoints(cfg.ProxyAddr, cfg.EndpointURLs)
		for _, item := range endpointResults {
			if !item.Reachable {
				allEndpointsOK = false
				break
			}
		}
	}

	shouldSwitch := false
	reason := ""

	if !currentFound {
		shouldSwitch = false
		reason = "current proxy not found"
	} else if !allEndpointsOK {
		failed := make([]string, 0)
		for _, item := range endpointResults {
			if !item.Reachable {
				failed = append(failed, item.URL)
			}
		}
		alt, found := findBestReachableAlternative(client, cfg, delays, current, cfg.EndpointURLs)
		if !found {
			alt, found = findBestAlternative(delays, current)
			if !found {
				shouldSwitch = false
				reason = "endpoints unreachable but no alternative proxy available"
			} else {
				shouldSwitch = true
				best = alt
				reason = "endpoints unreachable: " + strings.Join(failed, ", ") + "; fallback to fastest alternative without endpoint verification"
			}
		} else {
			shouldSwitch = true
			best = alt
			reason = "endpoints unreachable: " + strings.Join(failed, ", ") + "; switch to endpoint-verified alternative"
		}
	} else if currentDelay == nil {
		shouldSwitch = false
		reason = "current delay unavailable, keeping current"
	} else if *currentDelay <= cfg.KeepDelayThresholdMS {
		shouldSwitch = false
		reason = fmt.Sprintf("endpoints ok, delay %dms <= %dms threshold", *currentDelay, cfg.KeepDelayThresholdMS)
	} else {
		alt, found := findBestAlternative(delays, current)
		if !found {
			shouldSwitch = false
			reason = "no alternative proxy available"
		} else if (*currentDelay - alt.DelayMS) <= cfg.AutoSelectDiffMS {
			shouldSwitch = false
			reason = fmt.Sprintf("delay %dms > threshold but no significantly better option", *currentDelay)
		} else if len(cfg.EndpointURLs) == 0 {
			shouldSwitch = true
			best = alt
			reason = fmt.Sprintf("delay %dms > %dms and best is %dms faster", *currentDelay, cfg.KeepDelayThresholdMS, *currentDelay-alt.DelayMS)
		} else {
			reachableAlt, reachableFound := findBestReachableAlternative(client, cfg, delays, current, cfg.EndpointURLs)
			if !reachableFound {
				shouldSwitch = false
				reason = fmt.Sprintf("delay %dms > threshold but no endpoint-verified alternative", *currentDelay)
			} else if (*currentDelay - reachableAlt.DelayMS) <= cfg.AutoSelectDiffMS {
				shouldSwitch = false
				reason = fmt.Sprintf("delay %dms > threshold but no sufficiently faster endpoint-verified alternative", *currentDelay)
			} else {
				shouldSwitch = true
				best = reachableAlt
				reason = fmt.Sprintf("delay %dms > %dms and endpoint-verified best is %dms faster", *currentDelay, cfg.KeepDelayThresholdMS, *currentDelay-reachableAlt.DelayMS)
			}
		}
	}

	epSummary := make([]map[string]any, 0, len(endpointResults))
	for _, item := range endpointResults {
		epSummary = append(epSummary, map[string]any{
			"url":        item.URL,
			"reachable":  item.Reachable,
			"latency_ms": item.LatencyMS,
		})
	}

	if shouldSwitch && best.Name != current {
		if dryRun {
			result := map[string]any{
				"action":        "would_switch",
				"dry_run":       true,
				"from":          current,
				"to":            best.Name,
				"from_delay_ms": currentDelay,
				"to_delay_ms":   best.DelayMS,
				"reason":        reason,
				"endpoints":     epSummary,
			}
			if jsonOutput {
				fmt.Println(mustASCIIJSON(result))
				return
			}
			fromName := sanitizeName(current)
			toName := sanitizeName(best.Name)
			currentText := "nil"
			if currentDelay != nil {
				currentText = fmt.Sprintf("%dms", *currentDelay)
			}
			fmt.Printf("would_switch(dry-run)\t%s\t%s -> %dms\t%s\t(%s)\n", fromName, currentText, best.DelayMS, toName, reason)
			return
		}
		if err := switchProxy(client, cfg, best); err != nil {
			result := map[string]any{
				"action":        "switch_failed",
				"from":          current,
				"to":            best.Name,
				"from_delay_ms": currentDelay,
				"to_delay_ms":   best.DelayMS,
				"reason":        reason,
				"error":         err.Error(),
				"endpoints":     epSummary,
			}
			if jsonOutput {
				fmt.Println(mustASCIIJSON(result))
				return
			}
			fromName := sanitizeName(current)
			toName := sanitizeName(best.Name)
			currentText := "nil"
			if currentDelay != nil {
				currentText = fmt.Sprintf("%dms", *currentDelay)
			}
			fmt.Printf("switch_failed\t%s\t%s -> %dms\t%s\t(%s) err=%v\n", fromName, currentText, best.DelayMS, toName, reason, err)
			return
		}
		result := map[string]any{
			"action":        "switched",
			"from":          current,
			"to":            best.Name,
			"from_delay_ms": currentDelay,
			"to_delay_ms":   best.DelayMS,
			"reason":        reason,
			"endpoints":     epSummary,
		}
		if jsonOutput {
			fmt.Println(mustASCIIJSON(result))
			return
		}
		fromName := sanitizeName(current)
		toName := sanitizeName(best.Name)
		currentText := "nil"
		if currentDelay != nil {
			currentText = fmt.Sprintf("%dms", *currentDelay)
		}
		fmt.Printf("switched\t%s\t%s -> %dms\t%s\t(%s)\n", fromName, currentText, best.DelayMS, toName, reason)
		return
	}

	result := map[string]any{
		"action":        "kept",
		"current":       current,
		"delay_ms":      currentDelay,
		"best":          best.Name,
		"best_delay_ms": best.DelayMS,
		"reason":        reason,
		"endpoints":     epSummary,
	}
	if dryRun {
		result["dry_run"] = true
	}
	if jsonOutput {
		fmt.Println(mustASCIIJSON(result))
		return
	}
	currentText := "nil"
	if currentDelay != nil {
		currentText = fmt.Sprintf("%dms", *currentDelay)
	}
	fmt.Printf("kept\t%s\t%s\t(%s)\n", currentText, sanitizeName(current), reason)
}

func monitorLoop(client *http.Client, cfg Config, jsonOutput, dryRun bool) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-sigCh:
			log.Printf("Shutdown signal received")
			return
		default:
		}

		autoSelectOnce(client, cfg, jsonOutput, dryRun)

		timer := time.NewTimer(time.Duration(cfg.MonitorIntervalS) * time.Second)
		select {
		case <-sigCh:
			timer.Stop()
			log.Printf("Shutdown signal received")
			return
		case <-timer.C:
		}
	}
}

func checkEndpointsCurrentOnce(client *http.Client, cfg Config, jsonOutput bool) {
	current, currentFound := getCurrentProxy(client, cfg)

	if len(cfg.EndpointURLs) == 0 {
		if jsonOutput {
			fmt.Println(mustASCIIJSON(map[string]any{"error": "ENDPOINT_URLS is empty"}))
		} else {
			fmt.Println("ENDPOINT_URLS is empty")
		}
		return
	}

	if strings.TrimSpace(cfg.ProxyAddr) == "" {
		if jsonOutput {
			fmt.Println(mustASCIIJSON(map[string]any{"error": "MIHOMO_PROXY_ADDR is empty"}))
		} else {
			fmt.Println("MIHOMO_PROXY_ADDR is empty")
		}
		return
	}

	endpointResults := checkAllEndpoints(cfg.ProxyAddr, cfg.EndpointURLs)
	allReachable := true
	for _, item := range endpointResults {
		if !item.Reachable {
			allReachable = false
			break
		}
	}

	if jsonOutput {
		fmt.Println(mustASCIIJSON(map[string]any{
			"current":       current,
			"current_found": currentFound,
			"all_reachable": allReachable,
			"endpoints":     endpointResults,
		}))
		return
	}

	currentText := "unknown"
	if currentFound {
		currentText = sanitizeName(current)
	}
	status := "ok"
	if !allReachable {
		status = "degraded"
	}
	fmt.Printf("current\t%s\t%s\n", currentText, status)
	for _, item := range endpointResults {
		reachability := "unreachable"
		if item.Reachable {
			reachability = "reachable"
		}
		fmt.Printf("%s\t%dms\t%s\n", reachability, item.LatencyMS, item.URL)
	}
}

type CLIArgs struct {
	PrintDelays    bool
	JSONOutput     bool
	PrintCurrent   bool
	AutoSelect     bool
	Monitor        bool
	CheckEndpoints bool
	DryRun         bool
}

func parseArgs() (CLIArgs, error) {
	return parseArgsFrom(os.Args[1:])
}

func parseArgsFrom(argv []string) (CLIArgs, error) {
	var args CLIArgs
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&args.PrintDelays, "print-delays", false, "Print proxy delays for group and exit")
	fs.BoolVar(&args.JSONOutput, "json", false, "Use JSON output when printing delays")
	fs.BoolVar(&args.PrintCurrent, "print-current", false, "Print current proxy delay and exit")
	fs.BoolVar(&args.AutoSelect, "auto-select", false, "Auto select faster proxy and exit")
	fs.BoolVar(&args.Monitor, "monitor", false, "Run monitor loop with auto selection")
	fs.BoolVar(&args.CheckEndpoints, "check-endpoints", false, "Test ENDPOINT_URLS via current proxy and exit")
	fs.BoolVar(&args.DryRun, "dry-run", false, "Evaluate switching decision without applying proxy change")
	if err := fs.Parse(argv); err != nil {
		return CLIArgs{}, err
	}

	actionCount := 0
	if args.PrintDelays {
		actionCount++
	}
	if args.PrintCurrent {
		actionCount++
	}
	if args.AutoSelect {
		actionCount++
	}
	if args.Monitor {
		actionCount++
	}
	if args.CheckEndpoints {
		actionCount++
	}

	if actionCount != 1 {
		return CLIArgs{}, errors.New("exactly one of --print-delays, --print-current, --auto-select, --monitor, --check-endpoints is required")
	}
	if args.DryRun && !(args.AutoSelect || args.Monitor) {
		return CLIArgs{}, errors.New("--dry-run can only be used with --auto-select or --monitor")
	}
	return args, nil
}

func usageText() string {
	return strings.TrimSpace(`
Usage:
  mihomo-monitor [--json] [--dry-run] (--print-delays | --print-current | --auto-select | --monitor | --check-endpoints)

Flags:
  --print-delays     Print top 10 proxy delays for group and exit
  --print-current    Print current proxy delay and exit
  --auto-select      Evaluate and switch proxy once
  --monitor          Run monitor loop with auto selection
  --check-endpoints  Test ENDPOINT_URLS via current proxy and exit
  --json             Use JSON output
  --dry-run          Only with --auto-select/--monitor; never apply switch
`)
}

func main() {
	log.SetFlags(log.LstdFlags)

	args, err := parseArgs()
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, err.Error())
			fmt.Fprintln(os.Stderr, usageText())
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, usageText())
		os.Exit(0)
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}

	baseTransport, err := buildBaseTransportNoEnvProxy()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	client := &http.Client{Transport: baseTransport}

	switch {
	case args.PrintDelays:
		printDelaysOnce(client, cfg, args.JSONOutput)
	case args.PrintCurrent:
		printCurrentDelayOnce(client, cfg, args.JSONOutput)
	case args.AutoSelect:
		autoSelectOnce(client, cfg, args.JSONOutput, args.DryRun)
	case args.Monitor:
		monitorLoop(client, cfg, args.JSONOutput, args.DryRun)
	case args.CheckEndpoints:
		checkEndpointsCurrentOnce(client, cfg, args.JSONOutput)
	}
}
