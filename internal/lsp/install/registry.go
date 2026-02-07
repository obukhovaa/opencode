package install

// BuiltinServers is the registry of built-in LSP server definitions.
var BuiltinServers = []ServerDefinition{
	// Go
	{
		ID:             "gopls",
		Extensions:     []string{".go"},
		Command:        []string{"gopls"},
		Strategy:       StrategyGoInstall,
		InstallPackage: "golang.org/x/tools/gopls@latest",
		DefaultInit: map[string]any{
			"codelenses": map[string]bool{
				"generate":           true,
				"regenerate_cgo":     true,
				"test":               true,
				"tidy":               true,
				"upgrade_dependency": true,
				"vendor":             true,
				"vulncheck":          false,
			},
		},
	},

	// TypeScript / JavaScript
	{
		ID:             "typescript",
		Extensions:     []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts"},
		Command:        []string{"typescript-language-server", "--stdio"},
		Strategy:       StrategyNpm,
		InstallPackage: "typescript-language-server typescript",
	},

	// Bash
	{
		ID:             "bash",
		Extensions:     []string{".sh", ".bash", ".zsh", ".ksh"},
		Command:        []string{"bash-language-server", "start"},
		Strategy:       StrategyNpm,
		InstallPackage: "bash-language-server",
	},

	// YAML
	{
		ID:             "yaml",
		Extensions:     []string{".yaml", ".yml"},
		Command:        []string{"yaml-language-server", "--stdio"},
		Strategy:       StrategyNpm,
		InstallPackage: "yaml-language-server",
	},

	// Vue
	{
		ID:             "vue",
		Extensions:     []string{".vue"},
		Command:        []string{"vue-language-server", "--stdio"},
		Strategy:       StrategyNpm,
		InstallPackage: "@vue/language-server",
	},

	// Svelte
	{
		ID:             "svelte",
		Extensions:     []string{".svelte"},
		Command:        []string{"svelteserver", "--stdio"},
		Strategy:       StrategyNpm,
		InstallPackage: "svelte-language-server",
	},

	// Astro
	{
		ID:             "astro",
		Extensions:     []string{".astro"},
		Command:        []string{"astro-ls", "--stdio"},
		Strategy:       StrategyNpm,
		InstallPackage: "@astrojs/language-server",
	},

	// Python
	{
		ID:             "pyright",
		Extensions:     []string{".py"},
		Command:        []string{"pyright-langserver", "--stdio"},
		Strategy:       StrategyNpm,
		InstallPackage: "pyright",
	},

	// PHP
	{
		ID:             "intelephense",
		Extensions:     []string{".php"},
		Command:        []string{"intelephense", "--stdio"},
		Strategy:       StrategyNpm,
		InstallPackage: "intelephense",
	},

	// Lua
	{
		ID:          "lua-ls",
		Extensions:  []string{".lua"},
		Command:     []string{"lua-language-server"},
		Strategy:    StrategyGitHubRelease,
		InstallRepo: "LuaLS/lua-language-server",
	},

	// Terraform
	{
		ID:          "terraform",
		Extensions:  []string{".tf", ".tfvars"},
		Command:     []string{"terraform-ls", "serve"},
		Strategy:    StrategyGitHubRelease,
		InstallRepo: "hashicorp/terraform-ls",
	},

	// Tinymist (Typst)
	{
		ID:          "tinymist",
		Extensions:  []string{".typ", ".typc"},
		Command:     []string{"tinymist", "lsp"},
		Strategy:    StrategyGitHubRelease,
		InstallRepo: "Myriad-Dreamin/tinymist",
	},

	// --- Servers that require pre-installation (StrategyNone) ---

	// Rust
	{
		ID:         "rust-analyzer",
		Extensions: []string{".rs"},
		Command:    []string{"rust-analyzer"},
	},

	// C/C++
	{
		ID:         "clangd",
		Extensions: []string{".c", ".cpp", ".cc", ".cxx", ".c++", ".h", ".hpp", ".hh", ".hxx", ".h++"},
		Command:    []string{"clangd"},
	},

	// Dart
	{
		ID:         "dart",
		Extensions: []string{".dart"},
		Command:    []string{"dart", "language-server", "--protocol=lsp"},
	},

	// Zig
	{
		ID:         "zls",
		Extensions: []string{".zig", ".zon"},
		Command:    []string{"zls"},
	},

	// OCaml
	{
		ID:         "ocamllsp",
		Extensions: []string{".ml", ".mli"},
		Command:    []string{"ocamllsp"},
	},

	// Nix
	{
		ID:         "nixd",
		Extensions: []string{".nix"},
		Command:    []string{"nixd"},
	},

	// Haskell
	{
		ID:         "hls",
		Extensions: []string{".hs", ".lhs"},
		Command:    []string{"haskell-language-server-wrapper", "--lsp"},
	},

	// Gleam
	{
		ID:         "gleam",
		Extensions: []string{".gleam"},
		Command:    []string{"gleam", "lsp"},
	},

	// Swift
	{
		ID:         "sourcekit-lsp",
		Extensions: []string{".swift"},
		Command:    []string{"sourcekit-lsp"},
	},

	// Kotlin
	{
		ID:         "kotlin-lsp",
		Extensions: []string{".kt", ".kts"},
		Command:    []string{"kotlin-lsp", "kotlin-language-server"},
	},

	// Clojure
	{
		ID:         "clojure-lsp",
		Extensions: []string{".clj", ".cljs", ".cljc", ".edn"},
		Command:    []string{"clojure-lsp"},
	},

	// Elixir
	{
		ID:         "elixir-ls",
		Extensions: []string{".ex", ".exs"},
		Command:    []string{"elixir-ls"},
	},

	// Java
	{
		ID:         "jdtls",
		Extensions: []string{".java"},
		Command:    []string{"jdtls"},
	},

	// Ruby
	{
		ID:         "ruby-lsp",
		Extensions: []string{".rb", ".rake", ".gemspec", ".ru"},
		Command:    []string{"ruby-lsp"},
	},

	// C#
	{
		ID:         "csharp",
		Extensions: []string{".cs"},
		Command:    []string{"csharp-ls"},
	},

	// F#
	{
		ID:         "fsharp",
		Extensions: []string{".fs", ".fsi", ".fsx", ".fsscript"},
		Command:    []string{"fsautocomplete", "--adaptive-lsp-server-enabled"},
	},

	// Prisma
	{
		ID:         "prisma",
		Extensions: []string{".prisma"},
		Command:    []string{"prisma-language-server", "--stdio"},
	},

	// Scala
	{
		ID:         "metals",
		Extensions: []string{".scala"},
		Command:    []string{"metals"},
	},
}
