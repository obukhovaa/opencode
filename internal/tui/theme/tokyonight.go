package theme

// TokyoNightTheme implements the Theme interface with Tokyo Night colors.
// It provides both dark and light variants.
type TokyoNightTheme struct {
	BaseTheme
}

// NewTokyoNightTheme creates a new instance of the Tokyo Night theme.
func NewTokyoNightTheme() *TokyoNightTheme {
	// Tokyo Night color palette
	// Dark mode colors
	darkBackground := "#222436"
	darkCurrentLine := "#1e2030"
	darkSelection := "#2f334d"
	darkForeground := "#c8d3f5"
	darkComment := "#636da6"
	darkRed := "#ff757f"
	darkOrange := "#ff966c"
	darkYellow := "#ffc777"
	darkGreen := "#c3e88d"
	darkCyan := "#86e1fc"
	darkBlue := "#82aaff"
	darkPurple := "#c099ff"
	darkBorder := "#3b4261"

	// Light mode colors (Tokyo Night Day)
	lightBackground := "#e1e2e7"
	lightCurrentLine := "#d5d6db"
	lightSelection := "#c8c9ce"
	lightForeground := "#3760bf"
	lightComment := "#848cb5"
	lightRed := "#f52a65"
	lightOrange := "#b15c00"
	lightYellow := "#8c6c3e"
	lightGreen := "#587539"
	lightCyan := "#007197"
	lightBlue := "#2e7de9"
	lightPurple := "#9854f1"
	lightBorder := "#a8aecb"

	theme := &TokyoNightTheme{}

	// Base colors
	theme.PrimaryColor = ThemeColor{
		Dark:  darkBlue,
		Light: lightBlue,
	}
	theme.SecondaryColor = ThemeColor{
		Dark:  darkPurple,
		Light: lightPurple,
	}
	theme.AccentColor = ThemeColor{
		Dark:  darkOrange,
		Light: lightOrange,
	}

	// Status colors
	theme.ErrorColor = ThemeColor{
		Dark:  darkRed,
		Light: lightRed,
	}
	theme.WarningColor = ThemeColor{
		Dark:  darkOrange,
		Light: lightOrange,
	}
	theme.SuccessColor = ThemeColor{
		Dark:  darkGreen,
		Light: lightGreen,
	}
	theme.InfoColor = ThemeColor{
		Dark:  darkBlue,
		Light: lightBlue,
	}

	// Text colors
	theme.TextColor = ThemeColor{
		Dark:  darkForeground,
		Light: lightForeground,
	}
	theme.TextMutedColor = ThemeColor{
		Dark:  darkComment,
		Light: lightComment,
	}
	theme.TextEmphasizedColor = ThemeColor{
		Dark:  darkYellow,
		Light: lightYellow,
	}

	// Background colors
	theme.BackgroundColor = ThemeColor{
		Dark:  darkBackground,
		Light: lightBackground,
	}
	theme.BackgroundSecondaryColor = ThemeColor{
		Dark:  darkCurrentLine,
		Light: lightCurrentLine,
	}
	theme.BackgroundDarkerColor = ThemeColor{
		Dark:  "#191B29", // Darker background from palette
		Light: "#f0f0f5", // Slightly lighter than background
	}

	// Border colors
	theme.BorderNormalColor = ThemeColor{
		Dark:  darkBorder,
		Light: lightBorder,
	}
	theme.BorderFocusedColor = ThemeColor{
		Dark:  darkBlue,
		Light: lightBlue,
	}
	theme.BorderDimColor = ThemeColor{
		Dark:  darkSelection,
		Light: lightSelection,
	}

	// Diff view colors
	theme.DiffAddedColor = ThemeColor{
		Dark:  "#4fd6be", // teal from palette
		Light: "#1e725c",
	}
	theme.DiffRemovedColor = ThemeColor{
		Dark:  "#c53b53", // red1 from palette
		Light: "#c53b53",
	}
	theme.DiffContextColor = ThemeColor{
		Dark:  "#828bb8", // fg_dark from palette
		Light: "#7086b5",
	}
	theme.DiffHunkHeaderColor = ThemeColor{
		Dark:  "#828bb8", // fg_dark from palette
		Light: "#7086b5",
	}
	theme.DiffHighlightAddedColor = ThemeColor{
		Dark:  "#b8db87", // git.add from palette
		Light: "#4db380",
	}
	theme.DiffHighlightRemovedColor = ThemeColor{
		Dark:  "#e26a75", // git.delete from palette
		Light: "#f52a65",
	}
	theme.DiffAddedBgColor = ThemeColor{
		Dark:  "#20303b",
		Light: "#d5e5d5",
	}
	theme.DiffRemovedBgColor = ThemeColor{
		Dark:  "#37222c",
		Light: "#f7d8db",
	}
	theme.DiffContextBgColor = ThemeColor{
		Dark:  darkBackground,
		Light: lightBackground,
	}
	theme.DiffLineNumberColor = ThemeColor{
		Dark:  "#545c7e", // dark3 from palette
		Light: "#848cb5",
	}
	theme.DiffAddedLineNumberBgColor = ThemeColor{
		Dark:  "#1b2b34",
		Light: "#c5d5c5",
	}
	theme.DiffRemovedLineNumberBgColor = ThemeColor{
		Dark:  "#2d1f26",
		Light: "#e7c8cb",
	}

	// Markdown colors
	theme.MarkdownTextColor = ThemeColor{
		Dark:  darkForeground,
		Light: lightForeground,
	}
	theme.MarkdownHeadingColor = ThemeColor{
		Dark:  darkPurple,
		Light: lightPurple,
	}
	theme.MarkdownLinkColor = ThemeColor{
		Dark:  darkBlue,
		Light: lightBlue,
	}
	theme.MarkdownLinkTextColor = ThemeColor{
		Dark:  darkCyan,
		Light: lightCyan,
	}
	theme.MarkdownCodeColor = ThemeColor{
		Dark:  darkGreen,
		Light: lightGreen,
	}
	theme.MarkdownBlockQuoteColor = ThemeColor{
		Dark:  darkYellow,
		Light: lightYellow,
	}
	theme.MarkdownEmphColor = ThemeColor{
		Dark:  darkYellow,
		Light: lightYellow,
	}
	theme.MarkdownStrongColor = ThemeColor{
		Dark:  darkOrange,
		Light: lightOrange,
	}
	theme.MarkdownHorizontalRuleColor = ThemeColor{
		Dark:  darkComment,
		Light: lightComment,
	}
	theme.MarkdownListItemColor = ThemeColor{
		Dark:  darkBlue,
		Light: lightBlue,
	}
	theme.MarkdownListEnumerationColor = ThemeColor{
		Dark:  darkCyan,
		Light: lightCyan,
	}
	theme.MarkdownImageColor = ThemeColor{
		Dark:  darkBlue,
		Light: lightBlue,
	}
	theme.MarkdownImageTextColor = ThemeColor{
		Dark:  darkCyan,
		Light: lightCyan,
	}
	theme.MarkdownCodeBlockColor = ThemeColor{
		Dark:  darkForeground,
		Light: lightForeground,
	}

	// Syntax highlighting colors
	theme.SyntaxCommentColor = ThemeColor{
		Dark:  darkComment,
		Light: lightComment,
	}
	theme.SyntaxKeywordColor = ThemeColor{
		Dark:  darkPurple,
		Light: lightPurple,
	}
	theme.SyntaxFunctionColor = ThemeColor{
		Dark:  darkBlue,
		Light: lightBlue,
	}
	theme.SyntaxVariableColor = ThemeColor{
		Dark:  darkRed,
		Light: lightRed,
	}
	theme.SyntaxStringColor = ThemeColor{
		Dark:  darkGreen,
		Light: lightGreen,
	}
	theme.SyntaxNumberColor = ThemeColor{
		Dark:  darkOrange,
		Light: lightOrange,
	}
	theme.SyntaxTypeColor = ThemeColor{
		Dark:  darkYellow,
		Light: lightYellow,
	}
	theme.SyntaxOperatorColor = ThemeColor{
		Dark:  darkCyan,
		Light: lightCyan,
	}
	theme.SyntaxPunctuationColor = ThemeColor{
		Dark:  darkForeground,
		Light: lightForeground,
	}

	return theme
}

func init() {
	// Register the Tokyo Night theme with the theme manager
	RegisterTheme("tokyonight", NewTokyoNightTheme())
}
