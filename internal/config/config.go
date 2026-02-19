// Package config manages application configuration from various sources.
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/spf13/viper"
)

// MCPType defines the type of MCP (Model Control Protocol) server.
type MCPType string

// Supported MCP types
const (
	MCPStdio MCPType = "stdio"
	MCPSse   MCPType = "sse"
	MCPHttp  MCPType = "http"
)

// MCPServer defines the configuration for a Model Control Protocol server.
type MCPServer struct {
	Command string            `json:"command"`
	Env     []string          `json:"env"`
	Args    []string          `json:"args"`
	Type    MCPType           `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type AgentName = string

type AgentMode string

const (
	AgentModeAgent    AgentMode = "agent"
	AgentModeSubagent AgentMode = "subagent"
)

const (
	AgentCoder      AgentName = "coder"
	AgentSummarizer AgentName = "summarizer"
	AgentExplorer   AgentName = "explorer"
	AgentDescriptor AgentName = "descriptor"
	AgentWorkhorse  AgentName = "workhorse"
	AgentHivemind   AgentName = "hivemind"
)

// AgentOutput defines structured output configuration for an agent.
type AgentOutput struct {
	Schema map[string]any `json:"schema,omitempty"`
}

// Agent defines configuration for different LLM models and their token limits.
type Agent struct {
	Model           models.ModelID  `json:"model"`
	MaxTokens       int64           `json:"maxTokens"`
	ReasoningEffort string          `json:"reasoningEffort"`      // For openai models low,medium,high
	Permission      map[string]any  `json:"permission,omitempty"` // tool name -> "allow" | {"pattern": "action"}
	Tools           map[string]bool `json:"tools,omitempty"`      // e.g., {"skill": false}
	Mode            AgentMode       `json:"mode,omitempty"`       // "agent" or "subagent"
	Name            string          `json:"name,omitempty"`
	Native          bool            `json:"native,omitempty"`
	Description     string          `json:"description,omitempty"`
	Prompt          string          `json:"prompt,omitempty"`
	Color           string          `json:"color,omitempty"`
	Hidden          bool            `json:"hidden,omitempty"`
	Disabled        bool            `json:"disabled,omitempty"`
	Output          *AgentOutput    `json:"output,omitempty"`
}

// Provider defines configuration for an LLM provider.
type Provider struct {
	APIKey   string            `json:"apiKey"`
	Disabled bool              `json:"disabled"`
	BaseURL  string            `json:"baseURL"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// Data defines storage configuration.
type Data struct {
	Directory string `json:"directory,omitempty"`
}

// LSPConfig defines configuration for Language Server Protocol integration.
type LSPConfig struct {
	Disabled       bool              `json:"disabled"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Extensions     []string          `json:"extensions,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Initialization any               `json:"initialization,omitempty"`
}

// TUIConfig defines the configuration for the Terminal User Interface.
type TUIConfig struct {
	Theme string `json:"theme,omitempty"`
}

// ShellConfig defines the configuration for the shell used by the bash tool.
type ShellConfig struct {
	Path string   `json:"path,omitempty"`
	Args []string `json:"args,omitempty"`
}

// ProviderType defines the type of session storage provider.
type ProviderType string

// Supported provider types
const (
	ProviderSQLite ProviderType = "sqlite"
	ProviderMySQL  ProviderType = "mysql"
)

// MySQLConfig defines MySQL-specific configuration.
type MySQLConfig struct {
	DSN                string `json:"dsn,omitempty"`
	Host               string `json:"host,omitempty"`
	Port               int    `json:"port,omitempty"`
	Database           string `json:"database,omitempty"`
	Username           string `json:"username,omitempty"`
	Password           string `json:"password,omitempty"`
	MaxConnections     int    `json:"maxConnections,omitempty"`
	MaxIdleConnections int    `json:"maxIdleConnections,omitempty"`
	ConnectionTimeout  int    `json:"connectionTimeout,omitempty"`
}

// SessionProviderConfig defines configuration for session storage.
type SessionProviderConfig struct {
	Type  ProviderType `json:"type,omitempty"`
	MySQL MySQLConfig  `json:"mysql,omitempty"`
}

// SkillsConfig defines configuration for skills.
type SkillsConfig struct {
	Paths []string `json:"paths,omitempty"` // Custom skill paths
}

// PermissionConfig defines permission configuration.
// Each tool key maps to either a simple string ("allow"/"deny"/"ask")
// or an object with glob pattern keys (e.g., {"*": "ask", "git *": "allow"}).
type PermissionConfig struct {
	Skill map[string]string `json:"skill,omitempty"` // Deprecated: use Rules instead
	Rules map[string]any    `json:"rules,omitempty"` // tool name -> "allow" | {"pattern": "action"}
}

// Config is the main configuration structure for the application.
type Config struct {
	Data               Data                              `json:"data"`
	WorkingDir         string                            `json:"wd,omitempty"`
	MCPServers         map[string]MCPServer              `json:"mcpServers,omitempty"`
	Providers          map[models.ModelProvider]Provider `json:"providers,omitempty"`
	LSP                map[string]LSPConfig              `json:"lsp,omitempty"`
	Agents             map[AgentName]Agent               `json:"agents,omitempty"`
	Debug              bool                              `json:"debug,omitempty"`
	DebugLSP           bool                              `json:"debugLSP,omitempty"`
	ContextPaths       []string                          `json:"contextPaths,omitempty"`
	TUI                TUIConfig                         `json:"tui"`
	Shell              ShellConfig                       `json:"shell,omitempty"`
	AutoCompact        bool                              `json:"autoCompact,omitempty"`
	DisableLSPDownload bool                              `json:"disableLSPDownload,omitempty"`
	SessionProvider    SessionProviderConfig             `json:"sessionProvider,omitempty"`
	Skills             *SkillsConfig                     `json:"skills,omitempty"`
	Permission         *PermissionConfig                 `json:"permission,omitempty"`
}

// Application constants
const (
	defaultDataDirectory = ".opencode"
	defaultLogLevel      = "info"
	appName              = "opencode"

	MaxTokensFallbackDefault = 4096
)

var defaultContextPaths = []string{
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
	"AGENTS.local.md",
	"AGENTS.md",
}

// for testability, TODO: extend with other methods when needed
type Configurator interface {
	WorkingDirectory() string
}

// Global configuration instance
var cfg *Config

// Reset clears the global configuration, allowing Load to be called again.
// This is intended for use in tests only.
func Reset() {
	cfg = nil
}

// Load initializes the configuration from environment variables and config files.
// If debug is true, debug mode is enabled and log level is set to debug.
// It returns an error if configuration loading fails.
func Load(workingDir string, debug bool) (*Config, error) {
	if cfg != nil {
		return cfg, nil
	}

	cfg = &Config{
		WorkingDir: workingDir,
		MCPServers: make(map[string]MCPServer),
		Providers:  make(map[models.ModelProvider]Provider),
		LSP:        make(map[string]LSPConfig),
	}

	configureViper()
	setDefaults(debug)

	// Read global config
	if err := readConfig(viper.ReadInConfig()); err != nil {
		return cfg, err
	}

	// Load and merge local config
	mergeLocalConfig(workingDir)

	setProviderDefaults()

	// Apply configuration to the struct
	if err := viper.Unmarshal(cfg); err != nil {
		return cfg, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	applyDefaultValues()
	defaultLevel := slog.LevelInfo
	if cfg.Debug {
		defaultLevel = slog.LevelDebug
	}
	if os.Getenv("OPENCODE_DEV_DEBUG") == "true" {
		loggingFile := fmt.Sprintf("%s/%s", cfg.Data.Directory, "debug.log")
		messagesPath := fmt.Sprintf("%s/%s", cfg.Data.Directory, "messages")

		// if file does not exist create it
		if _, err := os.Stat(loggingFile); os.IsNotExist(err) {
			if err := os.MkdirAll(cfg.Data.Directory, 0o755); err != nil {
				return cfg, fmt.Errorf("failed to create directory: %w", err)
			}
			if _, err := os.Create(loggingFile); err != nil {
				return cfg, fmt.Errorf("failed to create log file: %w", err)
			}
		}

		if _, err := os.Stat(messagesPath); os.IsNotExist(err) {
			if err := os.MkdirAll(messagesPath, 0o756); err != nil {
				return cfg, fmt.Errorf("failed to create directory: %w", err)
			}
		}
		logging.MessageDir = messagesPath

		sloggingFileWriter, err := os.OpenFile(loggingFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
		if err != nil {
			return cfg, fmt.Errorf("failed to open log file: %w", err)
		}
		// Configure logger
		logger := slog.New(slog.NewTextHandler(sloggingFileWriter, &slog.HandlerOptions{
			Level: defaultLevel,
		}))
		slog.SetDefault(logger)
	} else {
		// Configure logger
		logger := slog.New(slog.NewTextHandler(logging.NewWriter(), &slog.HandlerOptions{
			Level: defaultLevel,
		}))
		slog.SetDefault(logger)
	}

	// Validate configuration
	if err := Validate(); err != nil {
		return cfg, fmt.Errorf("config validation failed: %w", err)
	}

	if cfg.Agents == nil {
		cfg.Agents = make(map[AgentName]Agent)
	}

	// Backward compatibility: migrate old agent names
	migrateOldAgentNames()

	// Override the max tokens for descriptor agent
	if desc, ok := cfg.Agents[AgentDescriptor]; ok {
		desc.MaxTokens = 80
		cfg.Agents[AgentDescriptor] = desc
	}
	return cfg, nil
}

// configureViper sets up viper's configuration paths and environment variables.
func configureViper() {
	viper.SetConfigName(fmt.Sprintf(".%s", appName))
	viper.SetConfigType("json")
	viper.AddConfigPath("$HOME")
	viper.AddConfigPath(fmt.Sprintf("$XDG_CONFIG_HOME/%s", appName))
	viper.AddConfigPath(fmt.Sprintf("$HOME/.config/%s", appName))
	viper.SetEnvPrefix(strings.ToUpper(appName))
	viper.AutomaticEnv()
}

// setDefaults configures default values for configuration options.
func setDefaults(debug bool) {
	viper.SetDefault("data.directory", defaultDataDirectory)
	viper.SetDefault("contextPaths", defaultContextPaths)
	viper.SetDefault("tui.theme", "opencode")
	viper.SetDefault("autoCompact", true)

	// LSP download control
	if v := os.Getenv("OPENCODE_DISABLE_LSP_DOWNLOAD"); v == "true" || v == "1" {
		viper.Set("disableLSPDownload", true)
	}

	// Set default shell from environment or fallback to /bin/bash
	shellPath := os.Getenv("SHELL")
	if shellPath == "" {
		shellPath = "/bin/bash"
	}
	viper.SetDefault("shell.path", shellPath)
	viper.SetDefault("shell.args", []string{"-l"})

	// Session provider defaults
	viper.SetDefault("sessionProvider.type", "sqlite")
	viper.SetDefault("sessionProvider.mysql.port", 3306)
	viper.SetDefault("sessionProvider.mysql.maxConnections", 10)
	viper.SetDefault("sessionProvider.mysql.maxIdleConnections", 5)
	viper.SetDefault("sessionProvider.mysql.connectionTimeout", 30)

	// Environment variable overrides for session provider
	if providerType := os.Getenv("OPENCODE_SESSION_PROVIDER_TYPE"); providerType != "" {
		viper.Set("sessionProvider.type", providerType)
	}
	if mysqlDSN := os.Getenv("OPENCODE_MYSQL_DSN"); mysqlDSN != "" {
		viper.Set("sessionProvider.mysql.dsn", mysqlDSN)
	}

	if debug {
		viper.SetDefault("debug", true)
		viper.Set("log.level", "debug")
	} else {
		viper.SetDefault("debug", false)
		viper.SetDefault("log.level", defaultLogLevel)
	}
}

// setProviderDefaults configures LLM provider defaults based on provider provided by
// environment variables and configuration file.
func setProviderDefaults() {
	// Set all API keys we can find in the environment
	// Note: Viper does not default if the json apiKey is ""
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		viper.SetDefault("providers.anthropic.apiKey", apiKey)
	}
	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		viper.SetDefault("providers.openai.apiKey", apiKey)
	}
	if apiKey := os.Getenv("GEMINI_API_KEY"); apiKey != "" {
		viper.SetDefault("providers.gemini.apiKey", apiKey)
	}
	if apiKey := os.Getenv("VERTEXAI_PROJECT"); apiKey != "" {
		viper.SetDefault("providers.vertexai.project", apiKey)
	}
	if apiKey := os.Getenv("VERTEXAI_LOCATION"); apiKey != "" {
		viper.SetDefault("providers.vertexai.location", apiKey)
	}

	// Use this order to set the default models
	// 1. Anthropic
	// 2. OpenAI
	// 3. Google Gemini
	// 4. AWS Bedrock
	// 5. Google Cloud VertexAI

	// Anthropic configuration
	if key := viper.GetString("providers.anthropic.apiKey"); strings.TrimSpace(key) != "" {
		viper.SetDefault("agents.coder.model", models.Claude45Sonnet1M)
		viper.SetDefault("agents.summarizer.model", models.Claude45Sonnet1M)
		viper.SetDefault("agents.explorer.model", models.Claude45Sonnet1M)
		viper.SetDefault("agents.descriptor.model", models.Claude45Sonnet1M)
		viper.SetDefault("agents.workhorse.model", models.Claude45Sonnet1M)
		viper.SetDefault("agents.hivemind.model", models.Claude45Sonnet1M)
		return
	}

	// OpenAI configuration
	if key := viper.GetString("providers.openai.apiKey"); strings.TrimSpace(key) != "" {
		viper.SetDefault("agents.coder.model", models.GPT5)
		viper.SetDefault("agents.summarizer.model", models.GPT5)
		viper.SetDefault("agents.explorer.model", models.O4Mini)
		viper.SetDefault("agents.descriptor.model", models.O4Mini)
		viper.SetDefault("agents.workhorse.model", models.GPT5)
		viper.SetDefault("agents.hivemind.model", models.GPT5)
		return
	}

	// Google Gemini configuration
	if key := viper.GetString("providers.gemini.apiKey"); strings.TrimSpace(key) != "" {
		viper.SetDefault("agents.coder.model", models.Gemini30Pro)
		viper.SetDefault("agents.summarizer.model", models.Gemini30Pro)
		viper.SetDefault("agents.explorer.model", models.Gemini30Flash)
		viper.SetDefault("agents.descriptor.model", models.Gemini30Flash)
		viper.SetDefault("agents.workhorse.model", models.Gemini30Pro)
		viper.SetDefault("agents.hivemind.model", models.Gemini30Pro)
		return
	}

	// AWS Bedrock configuration
	if hasAWSCredentials() {
		viper.SetDefault("agents.coder.model", models.BedrockClaude45Sonnet)
		viper.SetDefault("agents.summarizer.model", models.BedrockClaude45Sonnet)
		viper.SetDefault("agents.explorer.model", models.BedrockClaude45Sonnet)
		viper.SetDefault("agents.descriptor.model", models.BedrockClaude45Sonnet)
		viper.SetDefault("agents.workhorse.model", models.BedrockClaude45Sonnet)
		viper.SetDefault("agents.hivemind.model", models.BedrockClaude45Sonnet)
		return
	}

	// Google Cloud VertexAI configuration
	if hasVertexAICredentials() {
		viper.SetDefault("agents.coder.model", models.VertexAIGemini30Pro)
		viper.SetDefault("agents.summarizer.model", models.VertexAIGemini30Pro)
		viper.SetDefault("agents.explorer.model", models.VertexAIGemini30Flash)
		viper.SetDefault("agents.descriptor.model", models.VertexAIGemini30Flash)
		viper.SetDefault("agents.workhorse.model", models.VertexAIGemini30Pro)
		viper.SetDefault("agents.hivemind.model", models.VertexAIGemini30Pro)
		return
	}
}

// hasAWSCredentials checks if AWS credentials are available in the environment.
func hasAWSCredentials() bool {
	// Check for explicit AWS credentials
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != "" {
		return true
	}

	// Check for AWS profile
	if os.Getenv("AWS_PROFILE") != "" || os.Getenv("AWS_DEFAULT_PROFILE") != "" {
		return true
	}

	// Check for AWS region
	if os.Getenv("AWS_REGION") != "" || os.Getenv("AWS_DEFAULT_REGION") != "" {
		return true
	}

	// Check if running on EC2 with instance profile
	if os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" ||
		os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "" {
		return true
	}

	return false
}

// hasVertexAICredentials checks if VertexAI credentials are available in the environment.
func hasVertexAICredentials() bool {
	// Check for explicit VertexAI parameters
	if os.Getenv("VERTEXAI_PROJECT") != "" && os.Getenv("VERTEXAI_LOCATION") != "" {
		return true
	}
	// Check for Google Cloud project and location
	if os.Getenv("GOOGLE_CLOUD_PROJECT") != "" && (os.Getenv("GOOGLE_CLOUD_REGION") != "" || os.Getenv("GOOGLE_CLOUD_LOCATION") != "") {
		return true
	}
	return false
}

// migrateOldAgentNames migrates deprecated agent names to new names.
func migrateOldAgentNames() {
	if cfg.Agents == nil {
		return
	}
	// Migrate "task" -> "explorer"
	if agent, ok := cfg.Agents["task"]; ok {
		if _, exists := cfg.Agents[AgentExplorer]; !exists {
			cfg.Agents[AgentExplorer] = agent
		}
		delete(cfg.Agents, "task")
		logging.Warn("agent name 'task' is deprecated, use 'explorer' instead")
	}
	// Migrate "title" -> "descriptor"
	if agent, ok := cfg.Agents["title"]; ok {
		if _, exists := cfg.Agents[AgentDescriptor]; !exists {
			cfg.Agents[AgentDescriptor] = agent
		}
		delete(cfg.Agents, "title")
		logging.Warn("agent name 'title' is deprecated, use 'descriptor' instead")
	}
}

// readConfig handles the result of reading a configuration file.
func readConfig(err error) error {
	if err == nil {
		return nil
	}

	// It's okay if the config file doesn't exist
	if _, ok := err.(viper.ConfigFileNotFoundError); ok {
		return nil
	}

	return fmt.Errorf("failed to read config: %w", err)
}

// mergeLocalConfig loads and merges configuration from the local directory.
func mergeLocalConfig(workingDir string) {
	local := viper.New()
	local.SetConfigName(fmt.Sprintf(".%s", appName))
	local.SetConfigType("json")
	local.AddConfigPath(workingDir)

	// Merge local config if it exists
	if err := local.ReadInConfig(); err == nil {
		viper.MergeConfigMap(local.AllSettings())
	}
}

// applyDefaultValues sets default values for configuration fields that need processing.
func applyDefaultValues() {
	// Set default MCP type if not specified
	for k, v := range cfg.MCPServers {
		if v.Type == "" {
			v.Type = MCPStdio
			cfg.MCPServers[k] = v
		}
	}
}

// It validates model IDs and providers, ensuring they are supported.
func validateAgent(cfg *Config, name AgentName, agent Agent) error {
	// Check if model exists
	model, modelExists := models.SupportedModels[agent.Model]
	if !modelExists {
		logging.Warn("unsupported model configured, reverting to default",
			"agent", name,
			"configured_model", agent.Model)

		// Set default model based on available providers
		if setDefaultModelForAgent(name) {
			logging.Info("set default model for agent", "agent", name, "model", cfg.Agents[name].Model)
		} else {
			return fmt.Errorf("no valid provider available for agent %s", name)
		}
		return nil
	}

	// Check if provider for the model is configured
	provider := model.Provider
	providerCfg, providerExists := cfg.Providers[provider]

	if !providerExists {
		// Provider not configured, check if we have environment variables
		apiKey := getProviderAPIKey(provider)
		if apiKey == "" {
			logging.Warn("provider not configured for model, reverting to default",
				"agent", name,
				"model", agent.Model,
				"provider", provider)

			// Set default model based on available providers
			if setDefaultModelForAgent(name) {
				logging.Info("set default model for agent", "agent", name, "model", cfg.Agents[name].Model)
			} else {
				return fmt.Errorf("no valid provider available for agent %s", name)
			}
		} else {
			// Add provider with API key from environment
			cfg.Providers[provider] = Provider{
				APIKey: apiKey,
			}
			logging.Info("added provider from environment", "provider", provider)
		}
	} else if providerCfg.Disabled || providerCfg.APIKey == "" {
		// Provider is disabled or has no API key
		logging.Warn("provider is disabled or has no API key, reverting to default",
			"agent", name,
			"model", agent.Model,
			"provider", provider)

		// Set default model based on available providers
		if setDefaultModelForAgent(name) {
			logging.Info("set default model for agent", "agent", name, "model", cfg.Agents[name].Model)
		} else {
			return fmt.Errorf("no valid provider available for agent %s", name)
		}
	}

	// Validate max tokens
	if agent.MaxTokens <= 0 {
		logging.Warn("invalid max tokens, setting to default",
			"agent", name,
			"model", agent.Model,
			"max_tokens", agent.MaxTokens)

		// Update the agent with default max tokens
		updatedAgent := cfg.Agents[name]
		if model.DefaultMaxTokens > 0 {
			updatedAgent.MaxTokens = model.DefaultMaxTokens
		} else {
			updatedAgent.MaxTokens = MaxTokensFallbackDefault
		}
		cfg.Agents[name] = updatedAgent
	} else if model.ContextWindow > 0 && agent.MaxTokens > model.ContextWindow/2 {
		// Ensure max tokens doesn't exceed half the context window (reasonable limit)
		logging.Warn("max tokens exceeds half the context window, adjusting",
			"agent", name,
			"model", agent.Model,
			"max_tokens", agent.MaxTokens,
			"context_window", model.ContextWindow)

		// Update the agent with adjusted max tokens
		updatedAgent := cfg.Agents[name]
		updatedAgent.MaxTokens = model.ContextWindow / 2
		cfg.Agents[name] = updatedAgent
	}

	// Validate reasoning effort for models that support reasoning
	if model.CanReason && provider == models.ProviderOpenAI || provider == models.ProviderLocal {
		if agent.ReasoningEffort == "" {
			// Set default reasoning effort for models that support it
			logging.Info("setting default reasoning effort for model that supports reasoning",
				"agent", name,
				"model", agent.Model)

			// Update the agent with default reasoning effort
			updatedAgent := cfg.Agents[name]
			updatedAgent.ReasoningEffort = "medium"
			cfg.Agents[name] = updatedAgent
		} else {
			// Check if reasoning effort is valid (low, medium, high)
			effort := strings.ToLower(agent.ReasoningEffort)
			if effort != "low" && effort != "medium" && effort != "high" {
				logging.Warn("invalid reasoning effort, setting to medium",
					"agent", name,
					"model", agent.Model,
					"reasoning_effort", agent.ReasoningEffort)

				// Update the agent with valid reasoning effort
				updatedAgent := cfg.Agents[name]
				updatedAgent.ReasoningEffort = "medium"
				cfg.Agents[name] = updatedAgent
			}
		}
	} else if model.CanReason && model.SupportsAdaptiveThinking {
		if agent.ReasoningEffort != "" {
			effort := strings.ToLower(agent.ReasoningEffort)
			if effort == "max" && !model.SupportsMaximumThinking {
				logging.Warn("model doesn't support 'max' reasoning effort, falling back to 'high'",
					"agent", name,
					"model", agent.Model)

				updatedAgent := cfg.Agents[name]
				updatedAgent.ReasoningEffort = "high"
				cfg.Agents[name] = updatedAgent
			} else if effort != "low" && effort != "medium" && effort != "high" && effort != "max" {
				logging.Warn("invalid reasoning effort for adaptive thinking model, setting to high",
					"agent", name,
					"model", agent.Model,
					"reasoning_effort", agent.ReasoningEffort)

				updatedAgent := cfg.Agents[name]
				updatedAgent.ReasoningEffort = "high"
				cfg.Agents[name] = updatedAgent
			}
		}
	} else if !model.CanReason && agent.ReasoningEffort != "" {
		// Model doesn't support reasoning but reasoning effort is set
		logging.Warn("model doesn't support reasoning but reasoning effort is set, ignoring",
			"agent", name,
			"model", agent.Model,
			"reasoning_effort", agent.ReasoningEffort)

		// Update the agent to remove reasoning effort
		updatedAgent := cfg.Agents[name]
		updatedAgent.ReasoningEffort = ""
		cfg.Agents[name] = updatedAgent
	}

	return nil
}

// Validate checks if the configuration is valid and applies defaults where needed.
func Validate() error {
	if cfg == nil {
		return fmt.Errorf("config not loaded")
	}

	// Validate session provider configuration
	if err := validateSessionProvider(); err != nil {
		return fmt.Errorf("session provider validation failed: %w", err)
	}

	// Validate agent models
	for name, agent := range cfg.Agents {
		if err := validateAgent(cfg, name, agent); err != nil {
			return err
		}
	}

	// Validate providers
	for provider, providerCfg := range cfg.Providers {
		if providerCfg.APIKey == "" && !providerCfg.Disabled {
			fmt.Printf("provider has no API key, marking as disabled %s", provider)
			logging.Warn("provider has no API key, marking as disabled", "provider", provider)
			providerCfg.Disabled = true
			cfg.Providers[provider] = providerCfg
		}
	}

	// Validate LSP configurations
	for language, lspConfig := range cfg.LSP {
		if lspConfig.Command == "" && !lspConfig.Disabled && len(lspConfig.Extensions) == 0 {
			logging.Warn("LSP configuration has no command, marking as disabled", "language", language)
			lspConfig.Disabled = true
			cfg.LSP[language] = lspConfig
		}
	}

	return nil
}

// validateSessionProvider validates the session provider configuration.
func validateSessionProvider() error {
	providerType := cfg.SessionProvider.Type
	if providerType == "" {
		providerType = ProviderSQLite
	}

	// Validate provider type
	if providerType != ProviderSQLite && providerType != ProviderMySQL {
		return fmt.Errorf("invalid session provider type: %s (must be 'sqlite' or 'mysql')", providerType)
	}

	// Validate MySQL configuration if MySQL is selected
	if providerType == ProviderMySQL {
		mysql := cfg.SessionProvider.MySQL

		// If DSN is provided, it takes precedence over individual fields
		if mysql.DSN == "" {
			// Validate individual connection fields
			if mysql.Host == "" {
				return fmt.Errorf("MySQL host is required when using MySQL session provider (or provide DSN)")
			}
			if mysql.Database == "" {
				return fmt.Errorf("MySQL database is required when using MySQL session provider (or provide DSN)")
			}
			if mysql.Username == "" {
				return fmt.Errorf("MySQL username is required when using MySQL session provider (or provide DSN)")
			}
			if mysql.Password == "" {
				return fmt.Errorf("MySQL password is required when using MySQL session provider (or provide DSN)")
			}
		}
	}

	return nil
}

// getProviderAPIKey gets the API key for a provider from environment variables
func getProviderAPIKey(provider models.ModelProvider) string {
	switch provider {
	case models.ProviderAnthropic:
		return os.Getenv("ANTHROPIC_API_KEY")
	case models.ProviderOpenAI:
		return os.Getenv("OPENAI_API_KEY")
	case models.ProviderGemini:
		return os.Getenv("GEMINI_API_KEY")
	case models.ProviderBedrock:
		if hasAWSCredentials() {
			return "aws-credentials-available"
		}
	case models.ProviderVertexAI:
		if hasVertexAICredentials() {
			return "vertex-ai-credentials-available"
		}
	}
	return ""
}

// setDefaultModelForAgent sets a default model for an agent based on available providers in desired preference order
func setDefaultModelForAgent(agent AgentName) bool {
	if hasVertexAICredentials() {
		switch agent {
		case AgentDescriptor:
			cfg.Agents[agent] = Agent{
				Model:     models.VertexAISonnet45M,
				MaxTokens: 80,
			}
		case AgentExplorer:
			cfg.Agents[agent] = Agent{
				Model:     models.VertexAIGemini30Pro,
				MaxTokens: models.VertexAIAnthropicModels[models.VertexAIGemini30Pro].DefaultMaxTokens,
			}
		default:
			cfg.Agents[agent] = Agent{
				Model:     models.VertexAISonnet45M,
				MaxTokens: models.VertexAIAnthropicModels[models.VertexAISonnet45M].DefaultMaxTokens,
			}
		}
		return true
	}

	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		maxTokens := models.AnthropicModels[models.Claude45Sonnet1M].DefaultMaxTokens
		if agent == AgentDescriptor {
			maxTokens = 80
		}
		cfg.Agents[agent] = Agent{
			Model:     models.Claude45Sonnet1M,
			MaxTokens: maxTokens,
		}
		return true
	}

	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		var model models.ModelID
		maxTokens := models.OpenAIModels[models.GPT5].DefaultMaxTokens
		reasoningEffort := ""

		switch agent {
		case AgentDescriptor:
			model = models.GPT5
			maxTokens = 80
		case AgentExplorer:
			model = models.O4Mini
		default:
			model = models.GPT5
		}

		// Check if model supports reasoning
		if modelInfo, ok := models.SupportedModels[model]; ok && modelInfo.CanReason {
			reasoningEffort = "medium"
		}

		cfg.Agents[agent] = Agent{
			Model:           model,
			MaxTokens:       maxTokens,
			ReasoningEffort: reasoningEffort,
		}
		return true
	}

	if apiKey := os.Getenv("GEMINI_API_KEY"); apiKey != "" {
		switch agent {
		case AgentDescriptor:
			cfg.Agents[agent] = Agent{
				Model:     models.Gemini30Flash,
				MaxTokens: 80,
			}
		default:
			cfg.Agents[agent] = Agent{
				Model:     models.Gemini30Pro,
				MaxTokens: models.VertexAIAnthropicModels[models.Gemini30Pro].DefaultMaxTokens,
			}
		}
		return true
	}

	if hasAWSCredentials() {
		maxTokens := int64(5000)
		if agent == AgentDescriptor {
			maxTokens = 80
		}

		cfg.Agents[agent] = Agent{
			Model:           models.BedrockClaude45Sonnet,
			MaxTokens:       maxTokens,
			ReasoningEffort: "medium", // Claude models support reasoning
		}
		return true
	}

	return false
}

func updateCfgFile(updateCfg func(config *Config)) error {
	if cfg == nil {
		return fmt.Errorf("config not loaded")
	}

	// Get the config file path
	configFile := viper.ConfigFileUsed()
	var configData []byte
	if configFile == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		configFile = filepath.Join(homeDir, fmt.Sprintf(".%s.json", appName))
		logging.Info("config file not found, creating new one", "path", configFile)
		configData = []byte(`{}`)
	} else {
		// Read the existing config file
		data, err := os.ReadFile(configFile)
		if err != nil {
			return fmt.Errorf("failed to read config file: %w", err)
		}
		configData = data
	}

	// Parse the JSON
	var userCfg *Config
	if err := json.Unmarshal(configData, &userCfg); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	updateCfg(userCfg)

	// Write the updated config back to file
	updatedData, err := json.MarshalIndent(userCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configFile, updatedData, 0o644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// Get returns the current configuration.
// It's safe to call this function multiple times.
func Get() *Config {
	return cfg
}

// WorkingDirectory returns the current working directory from the configuration.
func WorkingDirectory() string {
	if cfg == nil {
		panic("config not loaded")
	}
	return cfg.WorkingDir
}

func (c *Config) WorkingDirectory() string {
	return WorkingDirectory()
}

func UpdateAgentModel(agentName AgentName, modelID models.ModelID) error {
	if cfg == nil {
		panic("config not loaded")
	}

	existingAgentCfg := cfg.Agents[agentName]

	model, ok := models.SupportedModels[modelID]
	if !ok {
		return fmt.Errorf("model %s not supported", modelID)
	}

	maxTokens := existingAgentCfg.MaxTokens
	if model.DefaultMaxTokens > 0 {
		maxTokens = model.DefaultMaxTokens
	}

	newAgentCfg := existingAgentCfg
	newAgentCfg.Model = modelID
	newAgentCfg.MaxTokens = maxTokens
	cfg.Agents[agentName] = newAgentCfg

	if err := validateAgent(cfg, agentName, newAgentCfg); err != nil {
		// revert config update on failure
		cfg.Agents[agentName] = existingAgentCfg
		return fmt.Errorf("failed to update agent model: %w", err)
	}

	return updateCfgFile(func(config *Config) {
		if config.Agents == nil {
			config.Agents = make(map[AgentName]Agent)
		}
		config.Agents[agentName] = newAgentCfg
	})
}

// UpdateTheme updates the theme in the configuration and writes it to the config file.
func UpdateTheme(themeName string) error {
	if cfg == nil {
		return fmt.Errorf("config not loaded")
	}

	// Update the in-memory config
	cfg.TUI.Theme = themeName

	// Update the file config
	return updateCfgFile(func(config *Config) {
		config.TUI.Theme = themeName
	})
}
