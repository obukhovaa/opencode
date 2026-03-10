package theme

// DraculaTheme implements the Theme interface with Dracula colors.
// It provides both dark and light variants, though Dracula is primarily a dark theme.
type DraculaTheme struct {
	BaseTheme
}

// NewDraculaTheme creates a new instance of the Dracula theme.
func NewDraculaTheme() *DraculaTheme {
	// Dracula color palette
	// Official colors from https://draculatheme.com/
	darkBackground := "#282a36"
	darkCurrentLine := "#44475a"
	darkSelection := "#44475a"
	darkForeground := "#f8f8f2"
	darkComment := "#6272a4"
	darkCyan := "#8be9fd"
	darkGreen := "#50fa7b"
	darkOrange := "#ffb86c"
	darkPink := "#ff79c6"
	darkPurple := "#bd93f9"
	darkRed := "#ff5555"
	darkYellow := "#f1fa8c"
	darkBorder := "#44475a"

	// Light mode approximation (Dracula is primarily a dark theme)
	lightBackground := "#f8f8f2"
	lightCurrentLine := "#e6e6e6"
	lightSelection := "#d8d8d8"
	lightForeground := "#282a36"
	lightComment := "#6272a4"
	lightCyan := "#0097a7"
	lightGreen := "#388e3c"
	lightOrange := "#f57c00"
	lightPink := "#d81b60"
	lightPurple := "#7e57c2"
	lightRed := "#e53935"
	lightYellow := "#fbc02d"
	lightBorder := "#d8d8d8"

	theme := &DraculaTheme{}

	// Base colors
	theme.PrimaryColor = ThemeColor{
		Dark:  darkPurple,
		Light: lightPurple,
	}
	theme.SecondaryColor = ThemeColor{
		Dark:  darkPink,
		Light: lightPink,
	}
	theme.AccentColor = ThemeColor{
		Dark:  darkCyan,
		Light: lightCyan,
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
		Dark:  "#21222c", // Slightly darker than background
		Light: "#ffffff", // Slightly lighter than background
	}

	// Border colors
	theme.BorderNormalColor = ThemeColor{
		Dark:  darkBorder,
		Light: lightBorder,
	}
	theme.BorderFocusedColor = ThemeColor{
		Dark:  darkPurple,
		Light: lightPurple,
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
		Dark:  darkPurple,
		Light: lightPurple,
	}
	theme.DiffHighlightAddedColor = ThemeColor{
		Dark:  "#50fa7b",
		Light: "#a5d6a7",
	}
	theme.DiffHighlightRemovedColor = ThemeColor{
		Dark:  "#ff5555",
		Light: "#ef9a9a",
	}
	theme.DiffAddedBgColor = ThemeColor{
		Dark:  "#2c3b2c",
		Light: "#e8f5e9",
	}
	theme.DiffRemovedBgColor = ThemeColor{
		Dark:  "#3b2c2c",
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
		Dark:  "#253025",
		Light: "#c8e6c9",
	}
	theme.DiffRemovedLineNumberBgColor = ThemeColor{
		Dark:  "#302525",
		Light: "#ffcdd2",
	}

	// Markdown colors
	theme.MarkdownTextColor = ThemeColor{
		Dark:  darkForeground,
		Light: lightForeground,
	}
	theme.MarkdownHeadingColor = ThemeColor{
		Dark:  darkPink,
		Light: lightPink,
	}
	theme.MarkdownLinkColor = ThemeColor{
		Dark:  darkPurple,
		Light: lightPurple,
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
		Dark:  darkPurple,
		Light: lightPurple,
	}
	theme.MarkdownListEnumerationColor = ThemeColor{
		Dark:  darkCyan,
		Light: lightCyan,
	}
	theme.MarkdownImageColor = ThemeColor{
		Dark:  darkPurple,
		Light: lightPurple,
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
		Dark:  darkPink,
		Light: lightPink,
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
		Dark:  darkPurple,
		Light: lightPurple,
	}
	theme.SyntaxTypeColor = ThemeColor{
		Dark:  darkCyan,
		Light: lightCyan,
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
	// Register the Dracula theme with the theme manager
	RegisterTheme("dracula", NewDraculaTheme())
}
