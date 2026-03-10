package theme

// MonokaiProTheme implements the Theme interface with Monokai Pro colors.
// It provides both dark and light variants.
type MonokaiProTheme struct {
	BaseTheme
}

// NewMonokaiProTheme creates a new instance of the Monokai Pro theme.
func NewMonokaiProTheme() *MonokaiProTheme {
	// Monokai Pro color palette (dark mode)
	darkBackground := "#2d2a2e"
	darkCurrentLine := "#403e41"
	darkSelection := "#5b595c"
	darkForeground := "#fcfcfa"
	darkComment := "#727072"
	darkRed := "#ff6188"
	darkOrange := "#fc9867"
	darkYellow := "#ffd866"
	darkGreen := "#a9dc76"
	darkCyan := "#78dce8"
	darkBlue := "#ab9df2"
	darkPurple := "#ab9df2"
	darkBorder := "#403e41"

	// Light mode colors (adapted from dark)
	lightBackground := "#fafafa"
	lightCurrentLine := "#f0f0f0"
	lightSelection := "#e5e5e6"
	lightForeground := "#2d2a2e"
	lightComment := "#939293"
	lightRed := "#f92672"
	lightOrange := "#fd971f"
	lightYellow := "#e6db74"
	lightGreen := "#9bca65"
	lightCyan := "#66d9ef"
	lightBlue := "#7e75db"
	lightPurple := "#ae81ff"
	lightBorder := "#d3d3d3"

	theme := &MonokaiProTheme{}

	// Base colors
	theme.PrimaryColor = ThemeColor{
		Dark:  darkCyan,
		Light: lightCyan,
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
		Dark:  "#221f22", // Slightly darker than background
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
		Dark:  "#a9dc76",
		Light: "#9bca65",
	}
	theme.DiffRemovedColor = ThemeColor{
		Dark:  "#ff6188",
		Light: "#f92672",
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
		Dark:  "#c2e7a9",
		Light: "#c5e0b4",
	}
	theme.DiffHighlightRemovedColor = ThemeColor{
		Dark:  "#ff8ca6",
		Light: "#ffb3c8",
	}
	theme.DiffAddedBgColor = ThemeColor{
		Dark:  "#3a4a35",
		Light: "#e8f5e9",
	}
	theme.DiffRemovedBgColor = ThemeColor{
		Dark:  "#4a3439",
		Light: "#ffebee",
	}
	theme.DiffContextBgColor = ThemeColor{
		Dark:  darkBackground,
		Light: lightBackground,
	}
	theme.DiffLineNumberColor = ThemeColor{
		Dark:  "#888888",
		Light: "#9e9e9e",
	}
	theme.DiffAddedLineNumberBgColor = ThemeColor{
		Dark:  "#2d3a28",
		Light: "#c8e6c9",
	}
	theme.DiffRemovedLineNumberBgColor = ThemeColor{
		Dark:  "#3d2a2e",
		Light: "#ffcdd2",
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
		Dark:  darkCyan,
		Light: lightCyan,
	}
	theme.MarkdownLinkTextColor = ThemeColor{
		Dark:  darkBlue,
		Light: lightBlue,
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
		Dark:  darkCyan,
		Light: lightCyan,
	}
	theme.MarkdownListEnumerationColor = ThemeColor{
		Dark:  darkBlue,
		Light: lightBlue,
	}
	theme.MarkdownImageColor = ThemeColor{
		Dark:  darkCyan,
		Light: lightCyan,
	}
	theme.MarkdownImageTextColor = ThemeColor{
		Dark:  darkBlue,
		Light: lightBlue,
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
		Dark:  darkRed,
		Light: lightRed,
	}
	theme.SyntaxFunctionColor = ThemeColor{
		Dark:  darkGreen,
		Light: lightGreen,
	}
	theme.SyntaxVariableColor = ThemeColor{
		Dark:  darkForeground,
		Light: lightForeground,
	}
	theme.SyntaxStringColor = ThemeColor{
		Dark:  darkYellow,
		Light: lightYellow,
	}
	theme.SyntaxNumberColor = ThemeColor{
		Dark:  darkPurple,
		Light: lightPurple,
	}
	theme.SyntaxTypeColor = ThemeColor{
		Dark:  darkBlue,
		Light: lightBlue,
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
	// Register the Monokai Pro theme with the theme manager
	RegisterTheme("monokai", NewMonokaiProTheme())
}
