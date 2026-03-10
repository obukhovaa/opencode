package theme

// OpenCodeTheme implements the Theme interface with OpenCode brand colors.
// It provides both dark and light variants.
type OpenCodeTheme struct {
	BaseTheme
}

// NewOpenCodeTheme creates a new instance of the OpenCode theme.
func NewOpenCodeTheme() *OpenCodeTheme {
	// OpenCode color palette
	// Dark mode colors
	darkBackground := "#212121"
	darkCurrentLine := "#252525"
	darkSelection := "#303030"
	darkForeground := "#e0e0e0"
	darkComment := "#6a6a6a"
	darkPrimary := "#fab283"   // Primary orange/gold
	darkSecondary := "#5c9cf5" // Secondary blue
	darkAccent := "#9d7cd8"    // Accent purple
	darkRed := "#e06c75"       // Error red
	darkOrange := "#f5a742"    // Warning orange
	darkGreen := "#7fd88f"     // Success green
	darkCyan := "#56b6c2"      // Info cyan
	darkYellow := "#e5c07b"    // Emphasized text
	darkBorder := "#4b4c5c"    // Border color

	// Light mode colors
	lightBackground := "#f8f8f8"
	lightCurrentLine := "#f0f0f0"
	lightSelection := "#e5e5e6"
	lightForeground := "#2a2a2a"
	lightComment := "#8a8a8a"
	lightPrimary := "#3b7dd8"   // Primary blue
	lightSecondary := "#7b5bb6" // Secondary purple
	lightAccent := "#d68c27"    // Accent orange/gold
	lightRed := "#d1383d"       // Error red
	lightOrange := "#d68c27"    // Warning orange
	lightGreen := "#3d9a57"     // Success green
	lightCyan := "#318795"      // Info cyan
	lightYellow := "#b0851f"    // Emphasized text
	lightBorder := "#d3d3d3"    // Border color

	theme := &OpenCodeTheme{}

	// Base colors
	theme.PrimaryColor = ThemeColor{
		Dark:  darkPrimary,
		Light: lightPrimary,
	}
	theme.SecondaryColor = ThemeColor{
		Dark:  darkSecondary,
		Light: lightSecondary,
	}
	theme.AccentColor = ThemeColor{
		Dark:  darkAccent,
		Light: lightAccent,
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
		Dark:  "#121212", // Slightly darker than background
		Light: "#ffffff", // Slightly lighter than background
	}

	// Border colors
	theme.BorderNormalColor = ThemeColor{
		Dark:  darkBorder,
		Light: lightBorder,
	}
	theme.BorderFocusedColor = ThemeColor{
		Dark:  darkPrimary,
		Light: lightPrimary,
	}
	theme.BorderDimColor = ThemeColor{
		Dark:  darkSelection,
		Light: lightSelection,
	}

	// Diff view colors
	theme.DiffAddedColor = ThemeColor{
		Dark:  "#478247",
		Light: "#2E7D32",
	}
	theme.DiffRemovedColor = ThemeColor{
		Dark:  "#7C4444",
		Light: "#C62828",
	}
	theme.DiffContextColor = ThemeColor{
		Dark:  "#a0a0a0",
		Light: "#757575",
	}
	theme.DiffHunkHeaderColor = ThemeColor{
		Dark:  "#a0a0a0",
		Light: "#757575",
	}
	theme.DiffHighlightAddedColor = ThemeColor{
		Dark:  "#DAFADA",
		Light: "#A5D6A7",
	}
	theme.DiffHighlightRemovedColor = ThemeColor{
		Dark:  "#FADADD",
		Light: "#EF9A9A",
	}
	theme.DiffAddedBgColor = ThemeColor{
		Dark:  "#303A30",
		Light: "#E8F5E9",
	}
	theme.DiffRemovedBgColor = ThemeColor{
		Dark:  "#3A3030",
		Light: "#FFEBEE",
	}
	theme.DiffContextBgColor = ThemeColor{
		Dark:  darkBackground,
		Light: lightBackground,
	}
	theme.DiffLineNumberColor = ThemeColor{
		Dark:  "#888888",
		Light: "#9E9E9E",
	}
	theme.DiffAddedLineNumberBgColor = ThemeColor{
		Dark:  "#293229",
		Light: "#C8E6C9",
	}
	theme.DiffRemovedLineNumberBgColor = ThemeColor{
		Dark:  "#332929",
		Light: "#FFCDD2",
	}

	// Markdown colors
	theme.MarkdownTextColor = ThemeColor{
		Dark:  darkForeground,
		Light: lightForeground,
	}
	theme.MarkdownHeadingColor = ThemeColor{
		Dark:  darkSecondary,
		Light: lightSecondary,
	}
	theme.MarkdownLinkColor = ThemeColor{
		Dark:  darkPrimary,
		Light: lightPrimary,
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
		Dark:  darkAccent,
		Light: lightAccent,
	}
	theme.MarkdownHorizontalRuleColor = ThemeColor{
		Dark:  darkComment,
		Light: lightComment,
	}
	theme.MarkdownListItemColor = ThemeColor{
		Dark:  darkPrimary,
		Light: lightPrimary,
	}
	theme.MarkdownListEnumerationColor = ThemeColor{
		Dark:  darkCyan,
		Light: lightCyan,
	}
	theme.MarkdownImageColor = ThemeColor{
		Dark:  darkPrimary,
		Light: lightPrimary,
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
		Dark:  darkSecondary,
		Light: lightSecondary,
	}
	theme.SyntaxFunctionColor = ThemeColor{
		Dark:  darkPrimary,
		Light: lightPrimary,
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
		Dark:  darkAccent,
		Light: lightAccent,
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
	// Register the OpenCode theme with the theme manager
	RegisterTheme("opencode", NewOpenCodeTheme())
}
