package websearch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const maxKiroCLIOutputBytes = 8 << 20

type kiroCLIProvider struct {
	path    string
	timeout time.Duration
	apiKey  string
	region  string
}

func (p *kiroCLIProvider) Name() string { return ProviderKiroCLI }

func (p *kiroCLIProvider) Search(ctx context.Context, q Query) (results []Result, searchErr error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	workspace, err := os.MkdirTemp("", "kirocc-web-search-")
	if err != nil {
		return nil, fmt.Errorf("%w: create kiro-cli workspace: %w", ErrUnavailable, err)
	}
	defer func() {
		if err := os.RemoveAll(workspace); err != nil && searchErr == nil {
			results = nil
			searchErr = fmt.Errorf("%w: remove kiro-cli workspace: %w", ErrUnavailable, err)
		}
	}()

	prompt, err := kiroCLIPrompt(q)
	if err != nil {
		return nil, fmt.Errorf("%w: encode search query: %w", ErrUnavailable, err)
	}
	cmd := exec.CommandContext(ctx, p.path,
		"chat",
		"--v3",
		"--no-interactive",
		"--trust-tools=remote_web_search",
		"--output-format", "stream-json",
		prompt,
	)
	cmd.Dir = workspace
	cmd.Env = p.environment()

	var output cappedBuffer
	output.limit = maxKiroCLIOutputBytes
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: kiro-cli search: %w", ErrUnavailable, ctx.Err())
		}
		return nil, fmt.Errorf("%w: kiro-cli search failed: %v", ErrUnavailable, err)
	}
	if output.truncated {
		return nil, fmt.Errorf("%w: kiro-cli output exceeded %d bytes", ErrUnavailable, maxKiroCLIOutputBytes)
	}
	var found bool
	results, found, err = parseKiroCLIOutput(output.Bytes(), q.Text)
	if err != nil {
		return nil, fmt.Errorf("%w: parse kiro-cli search: %w", ErrUnavailable, err)
	}
	if !found {
		return nil, fmt.Errorf("%w: kiro-cli did not return a web_search result for the requested query", ErrUnavailable)
	}
	return results, nil
}

func kiroCLIPrompt(q Query) (string, error) {
	query, err := json.Marshal(q.Text)
	if err != nil {
		return "", err
	}
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Use the built-in remote_web_search tool exactly once to search for this exact query: %s.\n", query)
	if len(q.AllowedDomains) > 0 {
		fmt.Fprintf(&prompt, "Only return results from these domains: %s.\n", strings.Join(q.AllowedDomains, ", "))
	}
	if len(q.BlockedDomains) > 0 {
		fmt.Fprintf(&prompt, "Do not return results from these domains: %s.\n", strings.Join(q.BlockedDomains, ", "))
	}
	prompt.WriteString("Do not fetch pages or use any other tool. Finish immediately after the search.")
	return prompt.String(), nil
}

func (p *kiroCLIProvider) environment() []string {
	env := os.Environ()
	if p.apiKey != "" {
		env = setEnvironment(env, "KIRO_API_KEY", p.apiKey)
	}
	if p.region != "" {
		env = setEnvironment(env, "KIRO_API_REGION", p.region)
	}
	return env
}

func setEnvironment(env []string, name, value string) []string {
	prefix := name + "="
	filtered := env[:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, prefix+value)
}

type kiroCLIEvent struct {
	Type string `json:"type"`
	Data struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Title         string `json:"title"`
			Status        string `json:"status"`
			RawInput      struct {
				Query string `json:"query"`
			} `json:"rawInput"`
			RawOutput struct {
				Results []struct {
					Title         string `json:"title"`
					URL           string `json:"url"`
					Snippet       string `json:"snippet"`
					PublishedDate string `json:"publishedDate"`
				} `json:"results"`
			} `json:"rawOutput"`
		} `json:"update"`
	} `json:"data"`
}

func parseKiroCLIOutput(output []byte, query string) ([]Result, bool, error) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 64*1024), maxKiroCLIOutputBytes)
	for scanner.Scan() {
		var event kiroCLIEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		update := event.Data.Update
		if event.Type != "sessionUpdate" ||
			update.SessionUpdate != "tool_call_update" ||
			update.Title != "web_search" ||
			update.Status != "completed" {
			continue
		}
		if update.RawInput.Query != query {
			return nil, false, fmt.Errorf("%w: Kiro CLI searched a different query", ErrUnavailable)
		}
		results := make([]Result, 0, len(update.RawOutput.Results))
		for _, result := range update.RawOutput.Results {
			results = appendResult(results, result.Title, result.URL, result.Snippet, result.PublishedDate)
		}
		return results, true, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, false, fmt.Errorf("read CLI event stream: %w", err)
	}
	return nil, false, nil
}

type cappedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining > 0 {
		n := len(p)
		if n > remaining {
			n = remaining
		}
		_, _ = b.Buffer.Write(p[:n])
	}
	if len(p) > remaining {
		b.truncated = true
	}
	return len(p), nil
}
