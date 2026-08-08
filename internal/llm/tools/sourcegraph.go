package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type SourcegraphParams struct {
	Query         string `json:"query"`
	Count         int    `json:"count,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	Timeout       int    `json:"timeout,omitempty"`
}

type sourcegraphTool struct {
	client *http.Client
}

const (
	SourcegraphToolName        = "sourcegraph"
	sourcegraphToolDescription = `Searches code across public repositories via Sourcegraph. Use to find real-world examples, implementations, and patterns in open source code.

Query syntax cheatsheet (filters combine; regex is supported):
- Scope: "repo:^github\.com/org/repo$" (also repo:org/repo@branch), "file:\.go$", "lang:go", "-repo:..."/"-file:..." to exclude
- Kind: "type:symbol" (definitions), "type:path" (filenames), "type:diff"/"type:commit" (history, with author:/message:/after:"1 month ago")
- Content: bare terms match content; "content:\"exact string\"", "case:yes", boolean operators AND/OR/NOT with parentheses
- Output: "select:repo|file|content|symbol", "count:N"

Example: "repo:^github\.com/golang/go$ lang:go type:symbol context.WithTimeout"

Only public repositories; maximum 20 results per query.`
)

func NewSourcegraphTool() BaseTool {
	return &sourcegraphTool{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *sourcegraphTool) Info() ToolInfo {
	return ToolInfo{
		Name:        SourcegraphToolName,
		Description: sourcegraphToolDescription,
		Parameters: map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The Sourcegraph search query",
			},
			"count": map[string]any{
				"type":        "number",
				"description": "Optional number of results to return (default: 10, max: 20)",
			},
			"context_window": map[string]any{
				"type":        "number",
				"description": "The context around the match to return (default: 10 lines)",
			},
			"timeout": map[string]any{
				"type":        "number",
				"description": "Optional timeout in seconds (max 120)",
			},
		},
		Required: []string{"query"},
	}
}

func (t *sourcegraphTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params SourcegraphParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("Failed to parse sourcegraph parameters: " + err.Error()), nil
	}

	if params.Query == "" {
		return NewTextErrorResponse("Query parameter is required"), nil
	}

	if params.Count <= 0 {
		params.Count = 10
	} else if params.Count > 20 {
		params.Count = 20 // Limit to 20 results
	}

	if params.ContextWindow <= 0 {
		params.ContextWindow = 10 // Default context window
	}
	client := t.client
	if params.Timeout > 0 {
		maxTimeout := 120 // 2 minutes
		if params.Timeout > maxTimeout {
			params.Timeout = maxTimeout
		}
		client = &http.Client{
			Timeout: time.Duration(params.Timeout) * time.Second,
		}
	}

	type graphqlRequest struct {
		Query     string `json:"query"`
		Variables struct {
			Query string `json:"query"`
		} `json:"variables"`
	}

	request := graphqlRequest{
		Query: "query Search($query: String!) { search(query: $query, version: V2, patternType: keyword ) { results { matchCount, limitHit, resultCount, approximateResultCount, missing { name }, timedout { name }, indexUnavailable, results { __typename, ... on FileMatch { repository { name }, file { path, url, content }, lineMatches { preview, lineNumber, offsetAndLengths } } } } } }",
	}
	request.Variables.Query = params.Query

	graphqlQueryBytes, err := json.Marshal(request)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("failed to marshal GraphQL request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		"https://sourcegraph.com/.api/graphql",
		bytes.NewBuffer(graphqlQueryBytes),
	)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "opencode/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr == nil && len(body) > 0 {
			return NewTextErrorResponse(fmt.Sprintf("Request failed with status code: %d, response: %s", resp.StatusCode, string(body))), nil
		}

		return NewTextErrorResponse(fmt.Sprintf("Request failed with status code: %d", resp.StatusCode)), nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("failed to read response body: %w", err)
	}

	var result map[string]any
	if err = json.Unmarshal(body, &result); err != nil {
		return NewEmptyResponse(), fmt.Errorf("failed to unmarshal response: %w", err)
	}

	formattedResults, err := formatSourcegraphResults(result, params.Count, params.ContextWindow)
	if err != nil {
		return NewTextErrorResponse("Failed to format results: " + err.Error()), nil
	}

	return NewTextResponse(formattedResults), nil
}

func (t *sourcegraphTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
	return true
}

func (t *sourcegraphTool) IsBaseline() bool { return true }

func formatSourcegraphResults(result map[string]any, maxResults int, contextWindow int) (string, error) {
	var buffer strings.Builder

	if errors, ok := result["errors"].([]any); ok && len(errors) > 0 {
		buffer.WriteString("## Sourcegraph API Error\n\n")
		for _, err := range errors {
			if errMap, ok := err.(map[string]any); ok {
				if message, ok := errMap["message"].(string); ok {
					buffer.WriteString(fmt.Sprintf("- %s\n", message))
				}
			}
		}
		return buffer.String(), nil
	}

	data, ok := result["data"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("invalid response format: missing data field")
	}

	search, ok := data["search"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("invalid response format: missing search field")
	}

	searchResults, ok := search["results"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("invalid response format: missing results field")
	}

	matchCount, _ := searchResults["matchCount"].(float64)
	resultCount, _ := searchResults["resultCount"].(float64)
	limitHit, _ := searchResults["limitHit"].(bool)

	buffer.WriteString("# Sourcegraph Search Results\n\n")
	buffer.WriteString(fmt.Sprintf("Found %d matches across %d results\n", int(matchCount), int(resultCount)))

	if limitHit {
		buffer.WriteString("(Result limit reached, try a more specific query)\n")
	}

	buffer.WriteString("\n")

	results, ok := searchResults["results"].([]any)
	if !ok || len(results) == 0 {
		buffer.WriteString("No results found. Try a different query.\n")
		return buffer.String(), nil
	}

	if len(results) > maxResults {
		results = results[:maxResults]
	}

	for i, res := range results {
		fileMatch, ok := res.(map[string]any)
		if !ok {
			continue
		}

		typeName, _ := fileMatch["__typename"].(string)
		if typeName != "FileMatch" {
			continue
		}

		repo, _ := fileMatch["repository"].(map[string]any)
		file, _ := fileMatch["file"].(map[string]any)
		lineMatches, _ := fileMatch["lineMatches"].([]any)

		if repo == nil || file == nil {
			continue
		}

		repoName, _ := repo["name"].(string)
		filePath, _ := file["path"].(string)
		fileURL, _ := file["url"].(string)
		fileContent, _ := file["content"].(string)

		buffer.WriteString(fmt.Sprintf("## Result %d: %s/%s\n\n", i+1, repoName, filePath))

		if fileURL != "" {
			buffer.WriteString(fmt.Sprintf("URL: %s\n\n", fileURL))
		}

		if len(lineMatches) > 0 {
			for _, lm := range lineMatches {
				lineMatch, ok := lm.(map[string]any)
				if !ok {
					continue
				}

				lineNumber, _ := lineMatch["lineNumber"].(float64)
				preview, _ := lineMatch["preview"].(string)

				if fileContent != "" {
					lines := strings.Split(fileContent, "\n")

					buffer.WriteString("```\n")

					startLine := max(1, int(lineNumber)-contextWindow)

					for j := startLine - 1; j < int(lineNumber)-1 && j < len(lines); j++ {
						if j >= 0 {
							buffer.WriteString(fmt.Sprintf("%d| %s\n", j+1, lines[j]))
						}
					}

					buffer.WriteString(fmt.Sprintf("%d| %s\n", int(lineNumber), preview))

					endLine := int(lineNumber) + contextWindow

					for j := int(lineNumber); j < endLine && j < len(lines); j++ {
						buffer.WriteString(fmt.Sprintf("%d| %s\n", j+1, lines[j]))
					}

					buffer.WriteString("```\n\n")
				} else {
					buffer.WriteString("```\n")
					buffer.WriteString(fmt.Sprintf("%d| %s\n", int(lineNumber), preview))
					buffer.WriteString("```\n\n")
				}
			}
		}
	}

	return buffer.String(), nil
}
