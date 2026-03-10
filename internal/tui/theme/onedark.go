package theme

// OneDarkTheme implements the Theme interface with Atom's One Dark colors.
// It provides both dark and light variants.
type OneDarkTheme struct {
	BaseTheme
}

// NewOneDarkTheme creates a new instance of the One Dark theme.
func NewOneDarkTheme() *OneDarkTheme {
	// One Dark color palette
	// Dark mode colors from Atom One Dark
	darkBackground := "#282c34"
	darkCurrentLine := "#2c313c"
	darkSelection := "#3e4451"
	darkForeground := "#abb2bf"
	darkComment := "#5c6370"
	darkRed := "#e06c75"
	darkOrange := "#d19a66"
	darkYellow := "#e5c07b"
	darkGreen := "#98c379"
	darkCyan := "#56b6c2"
	darkBlue := "#61afef"
	darkPurple := "#c678dd"
	darkBorder := "#3b4048"

	// Light mode colors from Atom One Light
	lightBackground := "#fafafa"
	lightCurrentLine := "#f0f0f0"
	lightSelection := "#e5e5e6"
	lightForeground := "#383a42"
	lightComment := "#a0a1a7"
	lightRed := "#e45649"
	lightOrange := "#da8548"
	lightYellow := "#c18401"
	lightGreen := "#50a14f"
	lightCyan := "#0184bc"
	lightBlue := "#4078f2"
	lightPurple := "#a626a4"
	lightBorder := "#d3d3d3"

	theme := &OneDarkTheme{}

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
		Dark:  "#21252b", // Slightly darker than background
		Light: "#ffffff", // Slightly lighter than background
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
	// Register the One Dark theme with the theme manager
	RegisterTheme("onedark", NewOneDarkTheme())
}
