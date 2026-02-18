package main

import (
	"encoding/json"
	"fmt"
	"os"

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
				"tools": map[string]any{
					"type":        "object",
					"description": "Tool enable/disable configuration",
					"additionalProperties": map[string]any{
						"type":        "boolean",
						"description": "Whether the tool is enabled for this agent",
					},
				},
			},
			"required": []string{"model"},
		},
	}

	// Add model enum
	modelEnum := []string{}
	for modelID := range models.SupportedModels {
		modelEnum = append(modelEnum, string(modelID))
	}
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
