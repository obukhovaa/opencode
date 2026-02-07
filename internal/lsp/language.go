package lsp

import (
	"path/filepath"
	"strings"

	"github.com/opencode-ai/opencode/internal/lsp/protocol"
)

func DetectLanguageID(uri string) protocol.LanguageKind {
	ext := strings.ToLower(filepath.Ext(uri))
	switch ext {
	case ".abap":
		return protocol.LangABAP
	case ".bat":
		return protocol.LangWindowsBat
	case ".bib", ".bibtex":
		return protocol.LangBibTeX
	case ".clj", ".cljs", ".cljc", ".edn":
		return protocol.LangClojure
	case ".coffee":
		return protocol.LangCoffeescript
	case ".c":
		return protocol.LangC
	case ".cpp", ".cxx", ".cc", ".c++":
		return protocol.LangCPP
	case ".cs":
		return protocol.LangCSharp
	case ".css":
		return protocol.LangCSS
	case ".d":
		return protocol.LangD
	case ".pas", ".pascal":
		return protocol.LangDelphi
	case ".diff", ".patch":
		return protocol.LangDiff
	case ".dart":
		return protocol.LangDart
	case ".dockerfile":
		return protocol.LangDockerfile
	case ".ex", ".exs":
		return protocol.LangElixir
	case ".erl", ".hrl":
		return protocol.LangErlang
	case ".fs", ".fsi", ".fsx", ".fsscript":
		return protocol.LangFSharp
	case ".gitcommit":
		return protocol.LangGitCommit
	case ".gitrebase":
		return protocol.LangGitRebase
	case ".go":
		return protocol.LangGo
	case ".groovy":
		return protocol.LangGroovy
	case ".gleam":
		return protocol.LangGleam
	case ".hbs", ".handlebars":
		return protocol.LangHandlebars
	case ".hs", ".lhs":
		return protocol.LangHaskell
	case ".html", ".htm":
		return protocol.LangHTML
	case ".ini":
		return protocol.LangIni
	case ".java":
		return protocol.LangJava
	case ".js", ".mjs", ".cjs":
		return protocol.LangJavaScript
	case ".jsx":
		return protocol.LangJavaScriptReact
	case ".json":
		return protocol.LangJSON
	case ".kt", ".kts":
		return protocol.LangKotlin
	case ".tex", ".latex":
		return protocol.LangLaTeX
	case ".less":
		return protocol.LangLess
	case ".lua":
		return protocol.LangLua
	case ".makefile", "makefile":
		return protocol.LangMakefile
	case ".md", ".markdown":
		return protocol.LangMarkdown
	case ".m":
		return protocol.LangObjectiveC
	case ".mm":
		return protocol.LangObjectiveCPP
	case ".ml", ".mli":
		return protocol.LangOCaml
	case ".pl":
		return protocol.LangPerl
	case ".pm", ".pm6":
		return protocol.LangPerl6
	case ".php":
		return protocol.LangPHP
	case ".ps1", ".psm1":
		return protocol.LangPowershell
	case ".prisma":
		return protocol.LangPrisma
	case ".pug", ".jade":
		return protocol.LangPug
	case ".py":
		return protocol.LangPython
	case ".r":
		return protocol.LangR
	case ".cshtml", ".razor":
		return protocol.LangRazor
	case ".rb", ".rake", ".gemspec", ".ru":
		return protocol.LangRuby
	case ".erb":
		return protocol.LangERB
	case ".rs":
		return protocol.LangRust
	case ".scss":
		return protocol.LangSCSS
	case ".sass":
		return protocol.LangSASS
	case ".scala":
		return protocol.LangScala
	case ".shader":
		return protocol.LangShaderLab
	case ".sh", ".bash", ".zsh", ".ksh":
		return protocol.LangShellScript
	case ".sql":
		return protocol.LangSQL
	case ".svelte":
		return protocol.LangSvelte
	case ".swift":
		return protocol.LangSwift
	case ".ts", ".mts", ".cts":
		return protocol.LangTypeScript
	case ".tsx":
		return protocol.LangTypeScriptReact
	case ".tf":
		return protocol.LangTerraform
	case ".tfvars":
		return protocol.LangTerraformVars
	case ".hcl":
		return protocol.LangHCL
	case ".typ", ".typc":
		return protocol.LangTypst
	case ".xml":
		return protocol.LangXML
	case ".xsl":
		return protocol.LangXSL
	case ".yaml", ".yml":
		return protocol.LangYAML
	case ".vue":
		return protocol.LangVue
	case ".zig", ".zon":
		return protocol.LangZig
	case ".astro":
		return protocol.LangAstro
	case ".nix":
		return protocol.LangNix
	default:
		return protocol.LanguageKind("")
	}
}
