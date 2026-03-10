package theme

// Flexoki color palette constants
const (
	// Base colors
	flexokiPaper   = "#FFFCF0" // Paper (lightest)
	flexokiBase50  = "#F2F0E5" // bg-2 (light)
	flexokiBase100 = "#E6E4D9" // ui (light)
	flexokiBase150 = "#DAD8CE" // ui-2 (light)
	flexokiBase200 = "#CECDC3" // ui-3 (light)
	flexokiBase300 = "#B7B5AC" // tx-3 (light)
	flexokiBase500 = "#878580" // tx-2 (light)
	flexokiBase600 = "#6F6E69" // tx (light)
	flexokiBase700 = "#575653" // tx-3 (dark)
	flexokiBase800 = "#403E3C" // ui-3 (dark)
	flexokiBase850 = "#343331" // ui-2 (dark)
	flexokiBase900 = "#282726" // ui (dark)
	flexokiBase950 = "#1C1B1A" // bg-2 (dark)
	flexokiBlack   = "#100F0F" // bg (darkest)

	// Accent colors - Light theme (600)
	flexokiRed600     = "#AF3029"
	flexokiOrange600  = "#BC5215"
	flexokiYellow600  = "#AD8301"
	flexokiGreen600   = "#66800B"
	flexokiCyan600    = "#24837B"
	flexokiBlue600    = "#205EA6"
	flexokiPurple600  = "#5E409D"
	flexokiMagenta600 = "#A02F6F"

	// Accent colors - Dark theme (400)
	flexokiRed400     = "#D14D41"
	flexokiOrange400  = "#DA702C"
	flexokiYellow400  = "#D0A215"
	flexokiGreen400   = "#879A39"
	flexokiCyan400    = "#3AA99F"
	flexokiBlue400    = "#4385BE"
	flexokiPurple400  = "#8B7EC8"
	flexokiMagenta400 = "#CE5D97"
)

// FlexokiTheme implements the Theme interface with Flexoki colors.
// It provides both dark and light variants.
type FlexokiTheme struct {
	BaseTheme
}

// NewFlexokiTheme creates a new instance of the Flexoki theme.
func NewFlexokiTheme() *FlexokiTheme {
	theme := &FlexokiTheme{}

	// Base colors
	theme.PrimaryColor = ThemeColor{
		Dark:  flexokiBlue400,
		Light: flexokiBlue600,
	}
	theme.SecondaryColor = ThemeColor{
		Dark:  flexokiPurple400,
		Light: flexokiPurple600,
	}
	theme.AccentColor = ThemeColor{
		Dark:  flexokiOrange400,
		Light: flexokiOrange600,
	}

	// Status colors
	theme.ErrorColor = ThemeColor{
		Dark:  flexokiRed400,
		Light: flexokiRed600,
	}
	theme.WarningColor = ThemeColor{
		Dark:  flexokiYellow400,
		Light: flexokiYellow600,
	}
	theme.SuccessColor = ThemeColor{
		Dark:  flexokiGreen400,
		Light: flexokiGreen600,
	}
	theme.InfoColor = ThemeColor{
		Dark:  flexokiCyan400,
		Light: flexokiCyan600,
	}

	// Text colors
	theme.TextColor = ThemeColor{
		Dark:  flexokiBase300,
		Light: flexokiBase600,
	}
	theme.TextMutedColor = ThemeColor{
		Dark:  flexokiBase700,
		Light: flexokiBase500,
	}
	theme.TextEmphasizedColor = ThemeColor{
		Dark:  flexokiYellow400,
		Light: flexokiYellow600,
	}

	// Background colors
	theme.BackgroundColor = ThemeColor{
		Dark:  flexokiBlack,
		Light: flexokiPaper,
	}
	theme.BackgroundSecondaryColor = ThemeColor{
		Dark:  flexokiBase950,
		Light: flexokiBase50,
	}
	theme.BackgroundDarkerColor = ThemeColor{
		Dark:  flexokiBase900,
		Light: flexokiBase100,
	}

	// Border colors
	theme.BorderNormalColor = ThemeColor{
		Dark:  flexokiBase900,
		Light: flexokiBase100,
	}
	theme.BorderFocusedColor = ThemeColor{
		Dark:  flexokiBlue400,
		Light: flexokiBlue600,
	}
	theme.BorderDimColor = ThemeColor{
		Dark:  flexokiBase850,
		Light: flexokiBase150,
	}

	// Diff view colors
	theme.DiffAddedColor = ThemeColor{
		Dark:  flexokiGreen400,
		Light: flexokiGreen600,
	}
	theme.DiffRemovedColor = ThemeColor{
		Dark:  flexokiRed400,
		Light: flexokiRed600,
	}
	theme.DiffContextColor = ThemeColor{
		Dark:  flexokiBase700,
		Light: flexokiBase500,
	}
	theme.DiffHunkHeaderColor = ThemeColor{
		Dark:  flexokiBase700,
		Light: flexokiBase500,
	}
	theme.DiffHighlightAddedColor = ThemeColor{
		Dark:  flexokiGreen400,
		Light: flexokiGreen600,
	}
	theme.DiffHighlightRemovedColor = ThemeColor{
		Dark:  flexokiRed400,
		Light: flexokiRed600,
	}
	theme.DiffAddedBgColor = ThemeColor{
		Dark:  "#1D2419", // Darker green background
		Light: "#EFF2E2", // Light green background
	}
	theme.DiffRemovedBgColor = ThemeColor{
		Dark:  "#241919", // Darker red background
		Light: "#F2E2E2", // Light red background
	}
	theme.DiffContextBgColor = ThemeColor{
		Dark:  flexokiBlack,
		Light: flexokiPaper,
	}
	theme.DiffLineNumberColor = ThemeColor{
		Dark:  flexokiBase700,
		Light: flexokiBase500,
	}
	theme.DiffAddedLineNumberBgColor = ThemeColor{
		Dark:  "#1A2017", // Slightly darker green
		Light: "#E5EBD9", // Light green
	}
	theme.DiffRemovedLineNumberBgColor = ThemeColor{
		Dark:  "#201717", // Slightly darker red
		Light: "#EBD9D9", // Light red
	}

	// Markdown colors
	theme.MarkdownTextColor = ThemeColor{
		Dark:  flexokiBase300,
		Light: flexokiBase600,
	}
	theme.MarkdownHeadingColor = ThemeColor{
		Dark:  flexokiYellow400,
		Light: flexokiYellow600,
	}
	theme.MarkdownLinkColor = ThemeColor{
		Dark:  flexokiCyan400,
		Light: flexokiCyan600,
	}
	theme.MarkdownLinkTextColor = ThemeColor{
		Dark:  flexokiMagenta400,
		Light: flexokiMagenta600,
	}
	theme.MarkdownCodeColor = ThemeColor{
		Dark:  flexokiGreen400,
		Light: flexokiGreen600,
	}
	theme.MarkdownBlockQuoteColor = ThemeColor{
		Dark:  flexokiCyan400,
		Light: flexokiCyan600,
	}
	theme.MarkdownEmphColor = ThemeColor{
		Dark:  flexokiYellow400,
		Light: flexokiYellow600,
	}
	theme.MarkdownStrongColor = ThemeColor{
		Dark:  flexokiOrange400,
		Light: flexokiOrange600,
	}
	theme.MarkdownHorizontalRuleColor = ThemeColor{
		Dark:  flexokiBase800,
		Light: flexokiBase200,
	}
	theme.MarkdownListItemColor = ThemeColor{
		Dark:  flexokiBlue400,
		Light: flexokiBlue600,
	}
	theme.MarkdownListEnumerationColor = ThemeColor{
		Dark:  flexokiBlue400,
		Light: flexokiBlue600,
	}
	theme.MarkdownImageColor = ThemeColor{
		Dark:  flexokiPurple400,
		Light: flexokiPurple600,
	}
	theme.MarkdownImageTextColor = ThemeColor{
		Dark:  flexokiMagenta400,
		Light: flexokiMagenta600,
	}
	theme.MarkdownCodeBlockColor = ThemeColor{
		Dark:  flexokiBase300,
		Light: flexokiBase600,
	}

	// Syntax highlighting colors (based on Flexoki's mappings)
	theme.SyntaxCommentColor = ThemeColor{
		Dark:  flexokiBase700, // tx-3
		Light: flexokiBase300, // tx-3
	}
	theme.SyntaxKeywordColor = ThemeColor{
		Dark:  flexokiGreen400, // gr
		Light: flexokiGreen600, // gr
	}
	theme.SyntaxFunctionColor = ThemeColor{
		Dark:  flexokiOrange400, // or
		Light: flexokiOrange600, // or
	}
	theme.SyntaxVariableColor = ThemeColor{
		Dark:  flexokiBlue400, // bl
		Light: flexokiBlue600, // bl
	}
	theme.SyntaxStringColor = ThemeColor{
		Dark:  flexokiCyan400, // cy
		Light: flexokiCyan600, // cy
	}
	theme.SyntaxNumberColor = ThemeColor{
		Dark:  flexokiPurple400, // pu
		Light: flexokiPurple600, // pu
	}
	theme.SyntaxTypeColor = ThemeColor{
		Dark:  flexokiYellow400, // ye
		Light: flexokiYellow600, // ye
	}
	theme.SyntaxOperatorColor = ThemeColor{
		Dark:  flexokiBase500, // tx-2
		Light: flexokiBase500, // tx-2
	}
	theme.SyntaxPunctuationColor = ThemeColor{
		Dark:  flexokiBase500, // tx-2
		Light: flexokiBase500, // tx-2
	}

	return theme
}

func init() {
	// Register the Flexoki theme with the theme manager
	RegisterTheme("flexoki", NewFlexokiTheme())
}
