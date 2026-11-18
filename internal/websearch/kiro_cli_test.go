package websearch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseKiroCLIOutput(t *testing.T) {
	output := strings.Join([]string{
		"[INFO] KAS started",
		`{"type":"sessionUpdate","data":{"update":{"sessionUpdate":"tool_call","title":"web_search","rawInput":{"query":"weather"}}}}`,
		`{"type":"sessionUpdate","data":{"update":{"sessionUpdate":"tool_call_update","title":"web_search","status":"completed","rawInput":{"query":"weather"},"rawOutput":{"message":"done","results":[{"title":"Today","url":"https://example.com/weather","snippet":"Sunny","publishedDate":"2026-11-18"},{"title":"No URL","url":"file:///tmp/nope"}]}}}}`,
	}, "\n")

	results, found, err := parseKiroCLIOutput([]byte(output), "weather")
	if err != nil {
		t.Fatalf("parseKiroCLIOutput: %v", err)
	}
	if !found {
		t.Fatal("found = false, want completed web_search")
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v, want invalid URL dropped", results)
	}
	if results[0] != (Result{
		Title:   "Today",
		URL:     "https://example.com/weather",
		Snippet: "Sunny",
		PageAge: "2026-11-18",
	}) {
		t.Errorf("result = %+v, want parsed search hit", results[0])
	}
}

func TestParseKiroCLIOutputRejectsDifferentQuery(t *testing.T) {
	output := `{"type":"sessionUpdate","data":{"update":{"sessionUpdate":"tool_call_update","title":"web_search","status":"completed","rawInput":{"query":"different"},"rawOutput":{"results":[]}}}}`

	_, found, err := parseKiroCLIOutput([]byte(output), "weather")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if found {
		t.Error("found = true, want false for a different query")
	}
}

func TestKiroCLIPromptIncludesDomainRestrictions(t *testing.T) {
	prompt, err := kiroCLIPrompt(Query{
		Text:           `weather "today"`,
		AllowedDomains: []string{"weather.example"},
		BlockedDomains: []string{"spam.example"},
	})
	if err != nil {
		t.Fatalf("kiroCLIPrompt: %v", err)
	}
	for _, want := range []string{
		`"weather \"today\""`,
		"Only return results from these domains: weather.example.",
		"Do not return results from these domains: spam.example.",
		"exactly once",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt %q missing %q", prompt, want)
		}
	}
}

func TestSetEnvironmentReplacesExistingValue(t *testing.T) {
	env := setEnvironment(
		[]string{"PATH=/bin", "KIRO_API_KEY=old", "KIRO_API_KEY=older"},
		"KIRO_API_KEY",
		"new",
	)
	var keys []string
	for _, entry := range env {
		if strings.HasPrefix(entry, "KIRO_API_KEY=") {
			keys = append(keys, entry)
		}
	}
	if len(keys) != 1 || keys[0] != "KIRO_API_KEY=new" {
		t.Errorf("KIRO_API_KEY entries = %v, want only the override", keys)
	}
}

func TestCappedBufferMarksTruncation(t *testing.T) {
	var output cappedBuffer
	output.limit = 3
	if _, err := output.Write([]byte("abcd")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := output.String(); got != "abc" {
		t.Errorf("output = %q, want capped content", got)
	}
	if !output.truncated {
		t.Error("truncated = false, want true")
	}
}

func TestKiroCLIProviderSearch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX fake executable")
	}

	output := `{"type":"sessionUpdate","data":{"update":{"sessionUpdate":"tool_call_update","title":"web_search","status":"completed","rawInput":{"query":"weather"},"rawOutput":{"results":[{"title":"Today","url":"https://example.com/weather","snippet":"Sunny"}]}}}}`
	script := strings.Join([]string{
		"#!/bin/sh",
		`test "$1" = chat || exit 2`,
		`test "$2" = --v3 || exit 3`,
		`test "$4" = --trust-tools=remote_web_search || exit 4`,
		`test "$KIRO_API_KEY" = test-key || exit 5`,
		`test "$KIRO_API_REGION" = us-east-1 || exit 6`,
		`case "$7" in *weather*) ;; *) exit 7 ;; esac`,
		`printf '%s\n' '` + output + `'`,
	}, "\n")
	path := filepath.Join(t.TempDir(), "kiro-cli")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}

	provider := &kiroCLIProvider{
		path:    path,
		timeout: time.Second,
		apiKey:  "test-key",
		region:  "us-east-1",
	}
	results, err := provider.Search(context.Background(), Query{Text: "weather"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].URL != "https://example.com/weather" {
		t.Errorf("results = %+v, want the native search result", results)
	}
}
