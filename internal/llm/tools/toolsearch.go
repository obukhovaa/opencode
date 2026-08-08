package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
)

const (
	ToolSearchToolName = "toolsearch"

	toolSearchDescription = `Loads deferred tools so they can be called. Deferred tools are listed by name in a <system-reminder> block; their full schemas are not loaded until you search for them here.

- Query forms: an exact tool name, "select:name1,name2" for direct multi-select, "+term rest..." to require a term and rank by the rest, or plain keywords matched against tool names and descriptions.
- The result contains each matched tool's full contract; matched tools become callable on your next step.
- A query that matches nothing returns the list of still-deferred tool names.`

	// minKeywordScore filters noise matches on keyword queries.
	minKeywordScore = 2
)

// ToolSearchTool discovers and activates deferred tools on the client-side
// (fallback) path. The toolset it searches is bound after the agent's
// toolset resolves — the tool itself is created inside NewToolSet, before
// the full slice exists.
type ToolSearchTool struct {
	toolset atomic.Pointer[[]BaseTool]
}

type toolSearchParams struct {
	Query string `json:"query"`
}

func NewToolSearchTool() *ToolSearchTool { return &ToolSearchTool{} }

// BindToolset gives the tool the fully resolved toolset to search. Called
// by the agent once tool resolution completes.
func (t *ToolSearchTool) BindToolset(all []BaseTool) {
	t.toolset.Store(&all)
}

func (t *ToolSearchTool) Info() ToolInfo {
	return ToolInfo{
		Name:        ToolSearchToolName,
		Description: toolSearchDescription,
		Parameters: map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Tool name, \"select:name1,name2\", \"+required terms\", or keywords",
			},
		},
		Required: []string{"query"},
	}
}

func (t *ToolSearchTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool { return true }

func (t *ToolSearchTool) IsBaseline() bool { return true }

func (t *ToolSearchTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params toolSearchParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("invalid parameters"), nil
	}
	query := strings.TrimSpace(params.Query)
	if query == "" {
		return NewTextErrorResponse("query is required"), nil
	}

	ptr := t.toolset.Load()
	if ptr == nil {
		return NewTextErrorResponse("toolset is still loading; retry in a moment"), nil
	}
	sessionID, _ := GetContextValues(ctx)
	if sessionID == "" {
		// Activation is per session; without a session id it would be a
		// silent no-op while the reply claims success, leaving the tool
		// permanently uncallable. Fail loudly instead.
		return NewTextErrorResponse("toolsearch requires a session; none was found in context"), nil
	}

	deferred := map[string]*DeferredWrapper{} // still-deferred for this session
	active := map[string]bool{}               // already activated for this session
	for _, tool := range *ptr {
		w, ok := tool.(*DeferredWrapper)
		if !ok {
			continue
		}
		name := w.Info().Name
		if _, on := w.ActivatedAt(sessionID); on {
			active[name] = true
		} else {
			deferred[name] = w
		}
	}

	matches := matchDeferred(query, deferred)
	if len(matches) == 0 {
		var sb strings.Builder
		sb.WriteString("No deferred tools matched the query.")
		if hit := matchActivated(query, active); len(hit) > 0 {
			fmt.Fprintf(&sb, " Already loaded and directly callable: %s.", strings.Join(hit, ", "))
		}
		if len(deferred) > 0 {
			names := make([]string, 0, len(deferred))
			for n := range deferred {
				names = append(names, n)
			}
			sort.Strings(names)
			fmt.Fprintf(&sb, "\nStill-deferred tools: %s", strings.Join(names, ", "))
		} else {
			sb.WriteString("\nNo tools are currently deferred.")
		}
		return NewTextResponse(sb.String()), nil
	}

	var sb strings.Builder
	sb.WriteString("<system-reminder>\nThe following tools are now loaded and available to call:\n")
	for _, w := range matches {
		w.Activate(sessionID)
		info := w.Info()
		fmt.Fprintf(&sb, "\n## %s\n%s\n", info.Name, info.Description)
		if len(info.Parameters) > 0 {
			sb.WriteString("Parameters:\n")
			paramNames := make([]string, 0, len(info.Parameters))
			for p := range info.Parameters {
				paramNames = append(paramNames, p)
			}
			sort.Strings(paramNames)
			required := map[string]bool{}
			for _, r := range info.Required {
				required[r] = true
			}
			for _, p := range paramNames {
				desc, typ := "", ""
				if m, ok := info.Parameters[p].(map[string]any); ok {
					typ, _ = m["type"].(string)
					desc, _ = m["description"].(string)
				}
				req := "optional"
				if required[p] {
					req = "required"
				}
				fmt.Fprintf(&sb, "  - %s (%s, %s): %s\n", p, typ, req, desc)
			}
		}
	}
	sb.WriteString("</system-reminder>")
	return NewTextResponse(sb.String()), nil
}

// matchDeferred resolves a query against still-deferred tools: exact name,
// select: multi-select, then scored keyword search (+term requires a term).
func matchDeferred(query string, deferred map[string]*DeferredWrapper) []*DeferredWrapper {
	q := strings.ToLower(strings.TrimSpace(query))

	if rest, ok := strings.CutPrefix(q, "select:"); ok {
		var out []*DeferredWrapper
		for _, name := range strings.Split(rest, ",") {
			if w := lookupName(strings.TrimSpace(name), deferred); w != nil {
				out = append(out, w)
			}
		}
		return out
	}

	if w := lookupName(q, deferred); w != nil {
		return []*DeferredWrapper{w}
	}

	var requiredTerms, terms []string
	for _, tok := range strings.Fields(q) {
		if t, ok := strings.CutPrefix(tok, "+"); ok && t != "" {
			requiredTerms = append(requiredTerms, t)
			terms = append(terms, t)
		} else if tok != "" {
			terms = append(terms, tok)
		}
	}
	if len(terms) == 0 {
		return nil
	}

	type scored struct {
		w     *DeferredWrapper
		score int
		name  string
	}
	var results []scored
	for name, w := range deferred {
		info := w.Info()
		lname := strings.ToLower(name)
		ldesc := strings.ToLower(info.Description)
		score := 0
		missingRequired := false
		for _, t := range requiredTerms {
			if !strings.Contains(lname, t) && !strings.Contains(ldesc, t) {
				missingRequired = true
				break
			}
		}
		if missingRequired {
			continue
		}
		for _, t := range terms {
			switch {
			case lname == t:
				score += 10
			case strings.Contains(lname, t):
				score += 5
			case strings.Contains(ldesc, t):
				score += 2
			}
		}
		if score >= minKeywordScore {
			results = append(results, scored{w, score, name})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].name < results[j].name
	})
	out := make([]*DeferredWrapper, len(results))
	for i, r := range results {
		out[i] = r.w
	}
	return out
}

func lookupName(name string, deferred map[string]*DeferredWrapper) *DeferredWrapper {
	for n, w := range deferred {
		if strings.EqualFold(n, name) {
			return w
		}
	}
	return nil
}

func matchActivated(query string, active map[string]bool) []string {
	q := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(query, "select:")))
	var hit []string
	for name := range active {
		ln := strings.ToLower(name)
		for _, tok := range strings.Split(strings.ReplaceAll(q, ",", " "), " ") {
			tok = strings.TrimSpace(strings.TrimPrefix(tok, "+"))
			if tok != "" && (ln == tok || strings.Contains(ln, tok)) {
				hit = append(hit, name)
				break
			}
		}
	}
	sort.Strings(hit)
	return hit
}
