package theme

// TronTheme implements the Theme interface with Tron-inspired colors.
// It provides both dark and light variants, though Tron is primarily a dark theme.
type TronTheme struct {
	BaseTheme
}

// NewTronTheme creates a new instance of the Tron theme.
func NewTronTheme() *TronTheme {
	// Tron color palette
	// Inspired by the Tron movie's neon aesthetic
	darkBackground := "#0c141f"
	darkCurrentLine := "#1a2633"
	darkSelection := "#1a2633"
	darkForeground := "#caf0ff"
	darkComment := "#4d6b87"
	darkCyan := "#00d9ff"
	darkBlue := "#007fff"
	darkOrange := "#ff9000"
	darkPink := "#ff00a0"
	darkPurple := "#b73fff"
	darkRed := "#ff3333"
	darkYellow := "#ffcc00"
	darkGreen := "#00ff8f"
	darkBorder := "#1a2633"

	// Light mode approximation
	lightBackground := "#f0f8ff"
	lightCurrentLine := "#e0f0ff"
	lightSelection := "#d0e8ff"
	lightForeground := "#0c141f"
	lightComment := "#4d6b87"
	lightCyan := "#0097b3"
	lightBlue := "#0066cc"
	lightOrange := "#cc7300"
	lightPink := "#cc0080"
	lightPurple := "#9932cc"
	lightRed := "#cc2929"
	lightYellow := "#cc9900"
	lightGreen := "#00cc72"
	lightBorder := "#d0e8ff"

	theme := &TronTheme{}

	// Base colors
	theme.PrimaryColor = ThemeColor{
		Dark:  darkCyan,
		Light: lightCyan,
	}
	theme.SecondaryColor = ThemeColor{
		Dark:  darkBlue,
		Light: lightBlue,
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
		Dark:  darkCyan,
		Light: lightCyan,
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
		Dark:  "#070d14", // Slightly darker than background
		Light: "#ffffff", // Slightly lighter than background
	}

	// Border colors
	theme.BorderNormalColor = ThemeColor{
		Dark:  darkBorder,
		Light: lightBorder,
	}
	theme.BorderFocusedColor = ThemeColor{
		Dark:  darkCyan,
		Light: lightCyan,
	}
	theme.BorderDimColor = ThemeColor{
		Dark:  darkSelection,
		Light: lightSelection,
	}

	// Diff view colors
	theme.DiffAddedColor = ThemeColor{
		Dark:  darkGreen,
		Light: lightGreen,
	}
	theme.DiffRemovedColor = ThemeColor{
		Dark:  darkRed,
		Light: lightRed,
	}
	theme.DiffContextColor = ThemeColor{
		Dark:  darkComment,
		Light: lightComment,
	}
	theme.DiffHunkHeaderColor = ThemeColor{
		Dark:  darkBlue,
		Light: lightBlue,
	}
	theme.DiffHighlightAddedColor = ThemeColor{
		Dark:  "#00ff8f",
		Light: "#a5d6a7",
	}
	theme.DiffHighlightRemovedColor = ThemeColor{
		Dark:  "#ff3333",
		Light: "#ef9a9a",
	}
	theme.DiffAddedBgColor = ThemeColor{
		Dark:  "#0a2a1a",
		Light: "#e8f5e9",
	}
	theme.DiffRemovedBgColor = ThemeColor{
		Dark:  "#2a0a0a",
		Light: "#ffebee",
	}
	theme.DiffContextBgColor = ThemeColor{
		Dark:  darkBackground,
		Light: lightBackground,
	}
	theme.DiffLineNumberColor = ThemeColor{
		Dark:  darkComment,
		Light: lightComment,
	}
	theme.DiffAddedLineNumberBgColor = ThemeColor{
		Dark:  "#082015",
		Light: "#c8e6c9",
	}
	theme.DiffRemovedLineNumberBgColor = ThemeColor{
		Dark:  "#200808",
		Light: "#ffcdd2",
	}

	// Markdown colors
	theme.MarkdownTextColor = ThemeColor{
		Dark:  darkForeground,
		Light: lightForeground,
	}
	theme.MarkdownHeadingColor = ThemeColor{
		Dark:  darkCyan,
		Light: lightCyan,
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
		Dark:  darkCyan,
		Light: lightCyan,
	}
	theme.SyntaxFunctionColor = ThemeColor{
		Dark:  darkGreen,
		Light: lightGreen,
	}
	theme.SyntaxVariableColor = ThemeColor{
		Dark:  darkOrange,
		Light: lightOrange,
	}
	theme.SyntaxStringColor = ThemeColor{
		Dark:  darkYellow,
		Light: lightYellow,
	}
	theme.SyntaxNumberColor = ThemeColor{
		Dark:  darkBlue,
		Light: lightBlue,
	}
	theme.SyntaxTypeColor = ThemeColor{
		Dark:  darkPurple,
		Light: lightPurple,
	}
	theme.SyntaxOperatorColor = ThemeColor{
		Dark:  darkPink,
		Light: lightPink,
	}
	theme.SyntaxPunctuationColor = ThemeColor{
		Dark:  darkForeground,
		Light: lightForeground,
	}

	return theme
}

func init() {
	// Register the Tron theme with the theme manager
	RegisterTheme("tron", NewTronTheme())
}
