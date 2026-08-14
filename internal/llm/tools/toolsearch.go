package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"unicode"
)

const (
	ToolSearchToolName = "toolsearch"

	toolSearchDescription = `Loads deferred tools so they can be called. Deferred tools are listed by name in a <system-reminder> block; their full schemas are not loaded until you search for them here.

- Query forms: an exact tool name, "select:name1,name2" for direct multi-select, "+term rest..." to require a term and rank by the rest, or plain keywords matched against tool names and descriptions.
- The result contains each matched tool's full contract; matched tools become callable on your next step.
- A query that matches nothing returns the list of still-deferred tool names.`

	// minKeywordScore filters noise matches on keyword queries.
	minKeywordScore = 2

	// maxKeywordMatches bounds how many tools one keyword search can load.
	maxKeywordMatches = 12
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
	// Pattern is not part of this tool's declared schema. It is the argument
	// name of Anthropic's server-side tool_search_tool_regex, which models on
	// the native path have a schema for — they occasionally aim a call at this
	// tool's name while filling in that tool's argument. Accepting it turns a
	// wasted turn into a working search; `query` still wins when both are set.
	// The value is a REGEX, so it goes through normalizeRegexPattern before
	// reaching the literal matcher.
	Pattern string `json:"pattern"`
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
	pattern := strings.TrimSpace(params.Pattern)
	if query == "" && pattern == "" {
		return NewTextErrorResponse("query is required"), nil
	}
	if query == "" {
		// Normalizing to "" means the pattern carried no literal text (".*",
		// "_", "[a-z]+_[a-z]+"). Carry the empty query forward rather than
		// falling back to the raw pattern: an empty query matches nothing and
		// lands on the no-match branch, which lists the still-deferred names —
		// whereas handing "_" to a substring matcher would score against every
		// tool name and activate the entire deferred set.
		query = normalizeRegexPattern(pattern)
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

	matches, dropped := matchDeferred(query, deferred)
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
	if dropped > 0 {
		fmt.Fprintf(&sb, "\n%d further tools also matched and were NOT loaded (only the best-ranked %d are). Search again with a narrower query, or name them with \"select:\", if the one you need is missing.\n",
			dropped, maxKeywordMatches)
	}
	sb.WriteString("</system-reminder>")
	return NewTextResponse(sb.String()), nil
}

// Every regex construct below is replaced by a SPACE, never by "": the
// literal fragments around it are separate keyword terms, and gluing them
// together invents a term that matches nothing ("jira[a-z_]+comment" must
// become "jira comment", not "jiracomment").
var (
	// Inline flag / non-capturing group openers: `(?i)`, `(?im)`, `(?:`, `(?i:`.
	// Matched as a unit — dropping the punctuation alone would leave the flag
	// letters behind as a bogus term ("(?i)jira" -> "ijira").
	regexInlineGroup = regexp.MustCompile(`\(\?[a-zA-Z]*[:)]`)
	// Character classes and repeat specs, likewise whole: "[a-z0-9_]" must not
	// decay into the term "a-z0-9_".
	regexCharClass  = regexp.MustCompile(`\[[^\]]*\]`)
	regexRepeatSpec = regexp.MustCompile(`\{[^}]*\}`)
	// Escape classes (\w, \d, \s, \b...) carry no literal text.
	regexEscapeClass = regexp.MustCompile(`\\[a-zA-Z0-9]`)
	// What survives the group-level passes above: anchors, wildcards,
	// quantifiers, grouping punctuation, and escapes of literal characters.
	regexLeftovers = strings.NewReplacer(
		"^", " ", "$", " ", "(", " ", ")", " ", "*", " ", "+", " ", "?", " ", ".", " ", "\\", " ",
	)
)

// normalizeRegexPattern converts a tool_search_tool_regex `pattern` into a
// query this tool can match. That argument is a REGEX, while matching here is
// literal (exact name, then substring keywords) — so "jira_.*", "^jira" and
// "(?i)jira" would all match nothing and cost the very turn that accepting
// `pattern` at all is meant to save. Drop the regex syntax that carries no
// literal text, and split alternation into separate terms: the keyword matcher
// already ORs and ranks its terms, which is what `a|b` asks for.
//
// Only `pattern` is normalized, never `query` — this tool's own query syntax
// gives `+` a meaning (require this term) that the regex reading would eat.
func normalizeRegexPattern(pattern string) string {
	p := regexInlineGroup.ReplaceAllString(pattern, " ")
	p = regexCharClass.ReplaceAllString(p, " ")
	p = regexRepeatSpec.ReplaceAllString(p, " ")
	p = regexEscapeClass.ReplaceAllString(p, " ")

	var terms []string
	for _, alt := range strings.Split(p, "|") {
		// Fields also collapses the runs of spaces the replacements leave, so
		// a pattern that reduces to one token still hits the exact-name path.
		for _, term := range strings.Fields(regexLeftovers.Replace(alt)) {
			// A token the replacements left with no letters or digits ("_",
			// "__", "-") is regex debris, not a search term. Keeping it would
			// be worse than dropping the pattern: the matcher is substring-
			// based, so "_" scores against nearly every tool name and would
			// activate the ENTIRE deferred set — the context blowup deferral
			// exists to prevent. "[a-z]+_[a-z]+" reduces to exactly that, and
			// "^(gitlab|teamcity)_" would drag every other tool in alongside
			// the two it names.
			if strings.ContainsFunc(term, isAlnum) {
				terms = append(terms, term)
			}
		}
	}
	return strings.Join(terms, " ")
}

func isAlnum(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// matchDeferred resolves a query against still-deferred tools: exact name,
// select: multi-select, then scored keyword search (+term requires a term).
// The second return is how many keyword matches were dropped by the
// maxKeywordMatches cap, for the caller to report.
func matchDeferred(query string, deferred map[string]*DeferredWrapper) ([]*DeferredWrapper, int) {
	q := strings.ToLower(strings.TrimSpace(query))

	if rest, ok := strings.CutPrefix(q, "select:"); ok {
		var out []*DeferredWrapper
		for _, name := range strings.Split(rest, ",") {
			if w := lookupName(strings.TrimSpace(name), deferred); w != nil {
				out = append(out, w)
			}
		}
		// Never capped: the model named these tools one by one, so every hit
		// is something it explicitly asked to load.
		return out, 0
	}

	if w := lookupName(q, deferred); w != nil {
		return []*DeferredWrapper{w}, 0
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
		return nil, 0
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
	// Keyword scoring is substring-based and ORs its terms, so a broad query
	// ("mcp gitlab", or a regex whose namespace prefix survives normalization)
	// can score most of a large MCP fleet above minKeywordScore. Loading them
	// all would dump every schema into the context — the exact blowup deferral
	// exists to prevent — so keep the best-ranked slice and tell the model the
	// rest exist. Exact-name and select: hits bypass this entirely.
	dropped := 0
	if len(results) > maxKeywordMatches {
		dropped = len(results) - maxKeywordMatches
		results = results[:maxKeywordMatches]
	}
	out := make([]*DeferredWrapper, len(results))
	for i, r := range results {
		out[i] = r.w
	}
	return out, dropped
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
