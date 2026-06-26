package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
)

// JSONSchemaType represents a JSON Schema type
type JSONSchemaType struct {
	Type                 string           `json:"type,omitempty"`
	Description          string           `json:"description,omitempty"`
	Properties           map[string]any   `json:"properties,omitempty"`
	Required             []string         `json:"required,omitempty"`
	AdditionalProperties any              `json:"additionalProperties,omitempty"`
	Enum                 []any            `json:"enum,omitempty"`
	Items                map[string]any   `json:"items,omitempty"`
	OneOf                []map[string]any `json:"oneOf,omitempty"`
	AnyOf                []map[string]any `json:"anyOf,omitempty"`
	Default              any              `json:"default,omitempty"`
}

func main() {
	schema := generateSchema()

	// Pretty print the schema
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(schema); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding schema: %v\n", err)
		os.Exit(1)
	}
}

func generateSchema() map[string]any {
	schema := map[string]any{
		"$schema":     "http://json-schema.org/draft-07/schema#",
		"title":       "OpenCode Configuration",
		"description": "Configuration schema for the OpenCode application",
		"type":        "object",
		"properties":  map[string]any{},
	}

	// Add Data configuration
	schema["properties"].(map[string]any)["data"] = map[string]any{
		"type":        "object",
		"description": "Storage configuration",
		"properties": map[string]any{
			"directory": map[string]any{
				"type":        "string",
				"description": "Directory where application data is stored",
				"default":     ".opencode",
			},
		},
		"required": []string{"directory"},
	}

	// Add working directory
	schema["properties"].(map[string]any)["wd"] = map[string]any{
		"type":        "string",
		"description": "Working directory for the application",
	}

	// Add debug flags
	schema["properties"].(map[string]any)["debug"] = map[string]any{
		"type":        "boolean",
		"description": "Enable debug mode",
		"default":     false,
	}

	schema["properties"].(map[string]any)["debugLSP"] = map[string]any{
		"type":        "boolean",
		"description": "Enable LSP debug mode",
		"default":     false,
	}

	schema["properties"].(map[string]any)["contextPaths"] = map[string]any{
		"type":        "array",
		"description": "Context paths for the application",
		"items": map[string]any{
			"type": "string",
		},
		"default": []string{
			".github/copilot-instructions.md",
			".cursorrules",
			".cursor/rules/",
			"CLAUDE.md",
			"CLAUDE.local.md",
			"opencode.md",
			"opencode.local.md",
			"OpenCode.md",
			"OpenCode.local.md",
			"OPENCODE.md",
			"OPENCODE.local.md",
			"AGENTS.md",
			"AGENTS.local.md",
		},
	}

	schema["properties"].(map[string]any)["tui"] = map[string]any{
		"type":        "object",
		"description": "Terminal User Interface configuration",
		"properties": map[string]any{
			"theme": map[string]any{
				"type":        "string",
				"description": "TUI theme name",
				"default":     "opencode",
				"enum": []string{
					"opencode",
					"catppuccin",
					"dracula",
					"flexoki",
					"gruvbox",
					"monokai",
					"onedark",
					"tokyonight",
					"tron",
				},
			},
			"vimMode": map[string]any{
				"type":        "boolean",
				"description": "Enable vim-style keybindings for the chat text input",
				"default":     false,
			},
		},
	}

	// Add MCP servers
	schema["properties"].(map[string]any)["mcpServers"] = map[string]any{
		"type":        "object",
		"description": "Model Control Protocol server configurations",
		"additionalProperties": map[string]any{
			"type":        "object",
			"description": "MCP server configuration",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "Command to execute for the MCP server",
				},
				"env": map[string]any{
					"type":        "array",
					"description": "Environment variables for the MCP server",
					"items": map[string]any{
						"type": "string",
					},
				},
				"args": map[string]any{
					"type":        "array",
					"description": "Command arguments for the MCP server",
					"items": map[string]any{
						"type": "string",
					},
				},
				"type": map[string]any{
					"type":        "string",
					"description": "Type of MCP server",
					"enum":        []string{"stdio", "sse"},
					"default":     "stdio",
				},
				"url": map[string]any{
					"type":        "string",
					"description": "URL for SSE type MCP servers",
				},
				"headers": map[string]any{
					"type":        "object",
					"description": "HTTP headers for SSE type MCP servers",
					"additionalProperties": map[string]any{
						"type": "string",
					},
				},
				"disabled": map[string]any{
					"type":        "boolean",
					"description": "Whether the MCP server is disabled",
					"default":     false,
				},
				"callToolTimeoutSeconds": map[string]any{
					"type":        "integer",
					"description": "Per-tool-call timeout override in seconds. Zero or omitted falls back to the built-in default (5 minutes).",
					"minimum":     0,
				},
			},
			"required": []string{"command"},
		},
	}

	// Add providers
	providerSchema := map[string]any{
		"type":        "object",
		"description": "LLM provider configurations",
		"additionalProperties": map[string]any{
			"type":        "object",
			"description": "Provider configuration",
			"properties": map[string]any{
				"apiKey": map[string]any{
					"type":        "string",
					"description": "API key for the provider",
				},
				"disabled": map[string]any{
					"type":        "boolean",
					"description": "Whether the provider is disabled",
					"default":     false,
				},
				"baseURL": map[string]any{
					"type":        "string",
					"description": "Base URL for the provider instead of default one",
				},
				"headers": map[string]any{
					"type":        "object",
					"description": "Extra headers to attach to request",
					"additionalProperties": map[string]any{
						"type": "string",
					},
				},
				"metadata": map[string]any{
					"type":        "object",
					"description": "Metadata key-value pairs attached to every LLM API request body. Keys are built-in identifiers (sessionId, userId, tags) that OpenCode resolves at runtime. Values are the field names used in the metadata object sent to the API.",
					"properties": map[string]any{
						"sessionId": map[string]any{
							"type":        "string",
							"description": "Field name for the session ID in the metadata object. The value is resolved from the current session context.",
						},
						"userId": map[string]any{
							"type":        "string",
							"description": "Field name for the user ID in the metadata object. The value is read from OPENCODE_USER_ID env var, telemetry.userId config, or auto-generated as UUID at startup.",
						},
						"tags": map[string]any{
							"type":        "string",
							"description": "Field name for the tags array in the metadata object. Tags are resolved from telemetry.tags config and can be extended dynamically at runtime.",
						},
					},
					"additionalProperties": false,
				},
			},
		},
	}

	// Add known providers
	knownProviders := []string{
		string(models.ProviderAnthropic),
		string(models.ProviderOpenAI),
		string(models.ProviderGemini),
		string(models.ProviderBedrock),
		string(models.ProviderVertexAI),
	}

	providerSchema["additionalProperties"].(map[string]any)["properties"].(map[string]any)["provider"] = map[string]any{
		"type":        "string",
		"description": "Provider type",
		"enum":        knownProviders,
	}

	schema["properties"].(map[string]any)["providers"] = providerSchema

	// Add agents
	agentSchema := map[string]any{
		"type":        "object",
		"description": "Agent configurations",
		"additionalProperties": map[string]any{
			"type":        "object",
			"description": "Agent configuration",
			"properties": map[string]any{
				"model": map[string]any{
					"type":        "string",
					"description": "Model ID for the agent",
				},
				"maxTokens": map[string]any{
					"type":        "integer",
					"description": "Maximum tokens for the agent",
					"minimum":     1,
				},
				"reasoningEffort": map[string]any{
					"type":        "string",
					"description": "Reasoning effort for models that support it (OpenAI, Anthropic). 'max' is only available for models with maximum thinking support.",
					"enum":        []string{"low", "medium", "high", "max"},
				},
				"mode": map[string]any{
					"type":        "string",
					"description": "Agent mode: 'agent' for primary agents, 'subagent' for agents invoked by task tool",
					"enum":        []string{"agent", "subagent"},
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Display name for the agent",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Description of the agent's purpose",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "Custom system prompt for the agent",
				},
				"color": map[string]any{
					"type":        "string",
					"description": "Badge color for subagent display (e.g., 'blue', 'orange', 'primary', 'warning')",
				},
				"hidden": map[string]any{
					"type":        "boolean",
					"description": "Whether the agent is hidden from TUI agent switching",
					"default":     false,
				},
				"disabled": map[string]any{
					"type":        "boolean",
					"description": "Whether the agent is disabled and excluded from the registry entirely",
					"default":     false,
				},
				"permission": map[string]any{
					"type":        "object",
					"description": "Agent-specific permission overrides. Keys are tool names (e.g., 'bash', 'edit', 'skill'), values are either a simple action string or an object with glob-pattern keys",
					"additionalProperties": map[string]any{
						"anyOf": []map[string]any{
							{
								"type":        "string",
								"description": "Simple permission action",
								"enum":        []string{"allow", "deny", "ask"},
							},
							{
								"type":        "object",
								"description": "Granular permission patterns (glob-pattern keys to action values)",
								"additionalProperties": map[string]any{
									"type": "string",
									"enum": []string{"allow", "deny", "ask"},
								},
							},
						},
					},
				},
				"maxTurns": map[string]any{
					"type":        "integer",
					"description": "Maximum number of tool-use turns per request for this agent. Default is 100.",
					"minimum":     1,
				},
				"tools": map[string]any{
					"type":        "object",
					"description": "Tool enable/disable configuration",
					"additionalProperties": map[string]any{
						"type":        "boolean",
						"description": "Whether the tool is enabled for this agent",
					},
				},
				"parallelToolUse": map[string]any{
					"type":        "boolean",
					"description": "Whether to enable parallel tool execution for this agent. When true (default), independent tool calls run concurrently. Set to false to force sequential execution.",
					"default":     true,
				},
				"skills": map[string]any{
					"type":        "array",
					"description": "List of skill names to preload into the agent's system prompt at startup. Skills are injected as <skill_content> blocks. Only skills not explicitly denied by permissions are injected. Variable substitution and shell markup are not expanded for preloaded skills.",
					"items": map[string]any{
						"type": "string",
					},
				},
				"taskBudget": map[string]any{
					"type":        "integer",
					"description": "Advisory token budget for the full agentic loop (minimum 20000). Only supported by models with SupportsTaskBudget. The budget is carried across compaction via the remaining field.",
					"minimum":     20000,
				},
			},
			"required": []string{"model"},
		},
	}

	// Add model enum
	modelEnum := []string{}
	for modelID, info := range models.SupportedModels {
		if info.Provider != models.ProviderLocal {
			modelEnum = append(modelEnum, string(modelID))
		}
	}
	sort.Slice(modelEnum, func(i, j int) bool {
		mi := models.SupportedModels[models.ModelID(modelEnum[i])]
		mj := models.SupportedModels[models.ModelID(modelEnum[j])]
		if mi.Provider != mj.Provider {
			return mi.Provider < mj.Provider
		}
		return mi.Name < mj.Name
	})
	agentSchema["additionalProperties"].(map[string]any)["properties"].(map[string]any)["model"].(map[string]any)["enum"] = modelEnum

	// Add specific agent properties
	agentProperties := map[string]any{}
	knownAgents := []string{
		string(config.AgentCoder),
		string(config.AgentExplorer),
		string(config.AgentDescriptor),
		string(config.AgentSummarizer),
		string(config.AgentWorkhorse),
		string(config.AgentHivemind),
	}

	for _, agentName := range knownAgents {
		agentProperties[agentName] = map[string]any{
			"$ref": "#/definitions/agent",
		}
	}

	// Create a combined schema that allows both specific agents and additional ones
	combinedAgentSchema := map[string]any{
		"type":                 "object",
		"description":          "Agent configurations",
		"properties":           agentProperties,
		"additionalProperties": agentSchema["additionalProperties"],
	}

	schema["properties"].(map[string]any)["agents"] = combinedAgentSchema
	schema["definitions"] = map[string]any{
		"agent": agentSchema["additionalProperties"],
	}

	// Add LSP configuration
	schema["properties"].(map[string]any)["lsp"] = map[string]any{
		"type":        "object",
		"description": "Language Server Protocol configurations. Built-in servers are auto-detected; use this to override, disable, or add custom servers.",
		"additionalProperties": map[string]any{
			"type":        "object",
			"description": "LSP configuration for a language server",
			"properties": map[string]any{
				"disabled": map[string]any{
					"type":        "boolean",
					"description": "Whether the LSP server is disabled",
					"default":     false,
				},
				"command": map[string]any{
					"type":        "string",
					"description": "Command to execute for the LSP server",
				},
				"args": map[string]any{
					"type":        "array",
					"description": "Command arguments for the LSP server",
					"items": map[string]any{
						"type": "string",
					},
				},
				"extensions": map[string]any{
					"type":        "array",
					"description": "File extensions this LSP server should handle (e.g., [\".go\", \".mod\"])",
					"items": map[string]any{
						"type": "string",
					},
				},
				"env": map[string]any{
					"type":        "object",
					"description": "Environment variables to set when starting the LSP server",
					"additionalProperties": map[string]any{
						"type": "string",
					},
				},
				"initialization": map[string]any{
					"type":        "object",
					"description": "Initialization options sent to the LSP server during the initialize request. Options vary by server.",
				},
			},
		},
	}

	// Add disableLSPDownload flag
	schema["properties"].(map[string]any)["disableLSPDownload"] = map[string]any{
		"type":        "boolean",
		"description": "Disable automatic downloading and installation of LSP servers. Can also be set via OPENCODE_DISABLE_LSP_DOWNLOAD environment variable.",
		"default":     false,
	}

	// Add shell configuration
	schema["properties"].(map[string]any)["shell"] = map[string]any{
		"type":        "object",
		"description": "Shell configuration for the bash tool",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the shell executable",
			},
			"args": map[string]any{
				"type":        "array",
				"description": "Arguments to pass to the shell",
				"items": map[string]any{
					"type": "string",
				},
				"default": []string{"-l"},
			},
		},
	}

	// Add autoCompact flag
	schema["properties"].(map[string]any)["autoCompact"] = map[string]any{
		"type":        "boolean",
		"description": "Enable automatic compaction of session history",
		"default":     true,
	}

	// Add session provider configuration
	schema["properties"].(map[string]any)["sessionProvider"] = map[string]any{
		"type":        "object",
		"description": "Session storage provider configuration",
		"properties": map[string]any{
			"type": map[string]any{
				"type":        "string",
				"description": "Type of session storage provider",
				"enum":        []string{"sqlite", "mysql"},
				"default":     "sqlite",
			},
			"mysql": map[string]any{
				"type":        "object",
				"description": "MySQL-specific configuration",
				"properties": map[string]any{
					"dsn": map[string]any{
						"type":        "string",
						"description": "MySQL Data Source Name (DSN) connection string",
					},
					"host": map[string]any{
						"type":        "string",
						"description": "MySQL server host",
					},
					"port": map[string]any{
						"type":        "integer",
						"description": "MySQL server port",
						"default":     3306,
					},
					"database": map[string]any{
						"type":        "string",
						"description": "MySQL database name",
					},
					"username": map[string]any{
						"type":        "string",
						"description": "MySQL username",
					},
					"password": map[string]any{
						"type":        "string",
						"description": "MySQL password",
					},
					"maxConnections": map[string]any{
						"type":        "integer",
						"description": "Maximum number of open connections",
						"default":     10,
					},
					"maxIdleConnections": map[string]any{
						"type":        "integer",
						"description": "Maximum number of idle connections",
						"default":     5,
					},
					"connectionTimeout": map[string]any{
						"type":        "integer",
						"description": "Connection timeout in seconds",
						"default":     30,
					},
				},
			},
		},
	}

	// Add session cleanup configuration
	schema["properties"].(map[string]any)["sessionCleanup"] = map[string]any{
		"type":        "object",
		"description": "Session cleanup configuration for removing old sessions",
		"properties": map[string]any{
			"maxAge": map[string]any{
				"type":        "string",
				"description": "Maximum age of sessions before they are eligible for cleanup. Supports Go duration strings (e.g. \"720h\") plus \"d\" for days and \"y\" for years. Examples: \"720h\", \"30d\", \"1y\". Defaults to \"30d\" (720h).",
				"default":     "30d",
			},
		},
		"additionalProperties": false,
	}

	// Add skills configuration
	schema["properties"].(map[string]any)["skills"] = map[string]any{
		"type":        "object",
		"description": "Skills configuration",
		"properties": map[string]any{
			"paths": map[string]any{
				"type":        "array",
				"description": "Custom paths to search for skills (supports ~ for home directory and relative paths)",
				"items": map[string]any{
					"type": "string",
				},
			},
		},
	}

	// Add web search configuration
	schema["properties"].(map[string]any)["webSearch"] = map[string]any{
		"type":        "object",
		"description": "Web search provider configuration",
		"properties": map[string]any{
			"providers": map[string]any{
				"type":        "object",
				"description": "Search provider configurations keyed by provider name",
				"additionalProperties": map[string]any{
					"type":        "object",
					"description": "Search provider configuration",
					"properties": map[string]any{
						"baseUrl": map[string]any{
							"type":        "string",
							"description": "Full URL to POST search queries to",
						},
						"apiKey": map[string]any{
							"type":        "string",
							"description": "API key for the provider. Supports 'env:VAR_NAME' syntax. Falls back to LOCAL_ENDPOINT_API_KEY env var.",
						},
						"description": map[string]any{
							"type":        "string",
							"description": "Human-readable description shown to the LLM to help select the right provider",
						},
					},
					"required": []string{"baseUrl"},
				},
			},
		},
	}

	// Add maxTurns at the top level (CLI override)
	schema["properties"].(map[string]any)["maxTurns"] = map[string]any{
		"type":        "integer",
		"description": "Global maximum number of agent tool-use turns per request. When set, overrides per-agent maxTurns. Also settable via --max-turns CLI flag.",
		"minimum":     1,
	}

	// Add telemetry configuration
	schema["properties"].(map[string]any)["telemetry"] = map[string]any{
		"type":        "object",
		"description": "Telemetry configuration for identifying requests. Values are used by provider metadata resolution.",
		"properties": map[string]any{
			"userId": map[string]any{
				"type":        "string",
				"description": "User identifier attached to LLM requests. Takes precedence over auto-generated UUID but is overridden by OPENCODE_USER_ID env var.",
			},
			"tags": map[string]any{
				"type":        "array",
				"description": "Static tags attached to every LLM request when the provider metadata has a tags field configured. Useful for environment labels (e.g., 'prod', 'dev', 'team-a').",
				"items": map[string]any{
					"type": "string",
				},
			},
			"defaultTags": map[string]any{
				"type":        "array",
				"description": "List of predefined tag keys that OpenCode will automatically add to metadata. Supported keys: 'agent'. When set, only listed tag keys are included in dynamic metadata tags.",
				"items": map[string]any{
					"type": "string",
					"enum": []string{"agent"},
				},
			},
			"langfuse": map[string]any{
				"type":        "object",
				"description": "Langfuse observability integration. When enabled, traces and generations are sent directly to Langfuse for LLM observability.",
				"properties": map[string]any{
					"enabled": map[string]any{
						"type":        "boolean",
						"description": "Enable Langfuse tracing",
						"default":     false,
					},
					"publicKey": map[string]any{
						"type":        "string",
						"description": "Langfuse public key. Supports 'env:VAR_NAME' syntax. Falls back to LANGFUSE_PUBLIC_KEY env var.",
					},
					"secretKey": map[string]any{
						"type":        "string",
						"description": "Langfuse secret key. Supports 'env:VAR_NAME' syntax. Falls back to LANGFUSE_SECRET_KEY env var.",
					},
					"baseURL": map[string]any{
						"type":        "string",
						"description": "Langfuse host URL. Supports 'env:VAR_NAME' syntax. Falls back to LANGFUSE_BASE_URL env var, then https://cloud.langfuse.com.",
					},
				},
				"additionalProperties": false,
			},
			"tools": map[string]any{
				"type":        "object",
				"description": "Controls what tool call data is logged to the telemetry backend. When enabled is false, no tool input/output is logged.",
				"properties": map[string]any{
					"enabled": map[string]any{
						"type":        "boolean",
						"description": "Enable tool input/output logging. When false, no tool data is logged regardless of logInput/logOutput patterns.",
						"default":     false,
					},
					"logInput": map[string]any{
						"type":        "array",
						"description": "Tool name patterns controlling which tools have their input logged. Supports wildcards (e.g., 'datadog*', 'read', '*'). If empty, no tool inputs are logged.",
						"items": map[string]any{
							"type": "string",
						},
					},
					"logOutput": map[string]any{
						"type":        "array",
						"description": "Tool name patterns controlling which tools have their output logged. Supports wildcards (e.g., 'bash', 'grep', '*'). If empty, no tool outputs are logged.",
						"items": map[string]any{
							"type": "string",
						},
					},
				},
				"additionalProperties": false,
			},
			"flowArgs": map[string]any{
				"type":        "array",
				"description": "Top-level flow argument names to extract into Langfuse trace metadata. Supports wildcards (e.g., 'ticket_id', 'project*', '*'). Matched args appear as dedicated metadata fields on flow step traces.",
				"items": map[string]any{
					"type": "string",
				},
			},
			"metadataNamespace": map[string]any{
				"type":        "string",
				"description": "Prefix for custom (non-Langfuse-standard) metadata keys on traces and generations. When set, keys like flow_id and agent_id become namespace.flow_id, namespace.agent_id — grouping them visually in the Langfuse UI while keeping each value independently filterable. Empty (default) preserves flat keys.",
			},
		},
		"additionalProperties": false,
	}

	// Add hooks configuration (Claude-Code-compatible PreToolUse / PostToolUse
	// subprocess hooks). When updating this schema, also update
	// `internal/hooks/config.go` (MatcherGroup, HookEntry) and the docs at
	// docs/hooks.md so the on-disk JSON shape stays in sync with the loader.
	hookEntrySchema := map[string]any{
		"type":        "object",
		"description": "A single executable hook within a matcher group.",
		"properties": map[string]any{
			"type": map[string]any{
				"type":        "string",
				"description": "Hook implementation type. v1 supports `command` only; settings entries with any other type are loaded and silently skipped with a WARN log so a settings.json that targets Claude Code's other hook types still loads cleanly.",
				"enum":        []string{"command"},
				"default":     "command",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "Executable to spawn. When `args` is omitted, the value is passed to `sh -c \"…\"` (shell form). When `args` is present, the value is exec'd directly with `args` as argv[1:] (no shell tokenization).",
			},
			"args": map[string]any{
				"type":        "array",
				"description": "Optional argv tail. Presence switches the spawn from shell form to exec form — author-controlled inputs are passed through verbatim with no shell expansion.",
				"items":       map[string]any{"type": "string"},
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Per-hook timeout in seconds. Default 600. The runner SIGTERMs the process group on overrun, then SIGKILLs after a 2-second grace.",
				"minimum":     1,
			},
			"shell": map[string]any{
				"type":        "string",
				"description": "Override the shell binary used for shell-form invocations. Defaults to `bash` if available on PATH, else `sh`.",
			},
		},
		"required":             []string{"command"},
		"additionalProperties": false,
	}
	matcherGroupSchema := map[string]any{
		"type":        "object",
		"description": "A matcher group runs its inner `hooks` list sequentially when its matcher matches the triggering tool name.",
		"properties": map[string]any{
			"matcher": map[string]any{
				"type":        "string",
				"description": "Tool-name predicate. Empty / `*` matches every tool. A value composed only of `[A-Za-z0-9_, |]` is an exact name or `|`/`,`-separated list, compared case-insensitively. Anything else is a Go RE2 regex (case-sensitive unless `(?i)` is used). opencode tool names are lowercase (`bash`, `edit`, `write`, …); PascalCase matchers from Claude Code configs (`Bash`, `Edit|Write`) also match.",
			},
			"hooks": map[string]any{
				"type":        "array",
				"description": "Sequentially-run hook entries.",
				"items":       hookEntrySchema,
			},
		},
		"required":             []string{"hooks"},
		"additionalProperties": false,
	}
	schema["properties"].(map[string]any)["hooks"] = map[string]any{
		"type":        "object",
		"description": "Claude-Code-compatible PreToolUse / PostToolUse subprocess hooks. Keys are event names (`PreToolUse`, `PostToolUse`); values are matcher groups whose entries fire as POSIX subprocesses receiving event JSON on stdin and returning decisions on stdout. The block is loaded once at process startup; restart required to pick up edits. Shape matches Claude Code's `settings.json` `hooks` block byte-for-byte for the events implemented here. See docs/hooks.md and openspec/specs/hook-runtime/spec.md.",
		"properties": map[string]any{
			"PreToolUse": map[string]any{
				"type":        "array",
				"description": "Fires before tool dispatch. Hooks can mutate `tool_input`, deny the call (`permissionDecision: \"deny\"` or exit 2), or override the standard permission gate (`permissionDecision: \"allow\"`).",
				"items":       matcherGroupSchema,
			},
			"PostToolUse": map[string]any{
				"type":        "array",
				"description": "Fires after a tool's Run returns successfully. Hooks can replace `tool_output` (RTK-style log compaction) or append additional context to the next agent turn. Does NOT fire on tool error.",
				"items":       matcherGroupSchema,
			},
		},
		"additionalProperties": map[string]any{
			"type":        "array",
			"description": "Other Claude Code event names (SessionStart, UserPromptSubmit, Stop, etc.) load cleanly but do not yet fire in opencode v1.",
			"items":       matcherGroupSchema,
		},
	}

	// Add permission configuration
	schema["properties"].(map[string]any)["permission"] = map[string]any{
		"type":        "object",
		"description": "Global permission configuration. Keys are tool names (e.g., 'bash', 'edit', 'skill'). Values are either a simple action string or an object with glob-pattern keys.",
		"properties": map[string]any{
			"skill": map[string]any{
				"type":        "object",
				"description": "Skill permission patterns (supports wildcards like 'internal-*')",
				"additionalProperties": map[string]any{
					"type":        "string",
					"description": "Permission action",
					"enum":        []string{"allow", "deny", "ask"},
				},
			},
		},
		"additionalProperties": map[string]any{
			"anyOf": []map[string]any{
				{
					"type":        "string",
					"description": "Simple permission action for all uses of this tool",
					"enum":        []string{"allow", "deny", "ask"},
				},
				{
					"type":        "object",
					"description": "Granular permission patterns (glob-pattern keys to action values)",
					"additionalProperties": map[string]any{
						"type": "string",
						"enum": []string{"allow", "deny", "ask"},
					},
				},
			},
		},
	}

	return schema
}
