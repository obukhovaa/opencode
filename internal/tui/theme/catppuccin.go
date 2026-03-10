package theme

import (
	catppuccin "github.com/catppuccin/go"
)

// CatppuccinTheme implements the Theme interface with Catppuccin colors.
// It provides both dark (Mocha) and light (Latte) variants.
type CatppuccinTheme struct {
	BaseTheme
}

// NewCatppuccinTheme creates a new instance of the Catppuccin theme.
func NewCatppuccinTheme() *CatppuccinTheme {
	// Get the Catppuccin palettes
	mocha := catppuccin.Mocha
	latte := catppuccin.Latte

	theme := &CatppuccinTheme{}

	// Base colors
	theme.PrimaryColor = ThemeColor{
		Dark:  mocha.Blue().Hex,
		Light: latte.Blue().Hex,
	}
	theme.SecondaryColor = ThemeColor{
		Dark:  mocha.Mauve().Hex,
		Light: latte.Mauve().Hex,
	}
	theme.AccentColor = ThemeColor{
		Dark:  mocha.Peach().Hex,
		Light: latte.Peach().Hex,
	}

	// Status colors
	theme.ErrorColor = ThemeColor{
		Dark:  mocha.Red().Hex,
		Light: latte.Red().Hex,
	}
	theme.WarningColor = ThemeColor{
		Dark:  mocha.Peach().Hex,
		Light: latte.Peach().Hex,
	}
	theme.SuccessColor = ThemeColor{
		Dark:  mocha.Green().Hex,
		Light: latte.Green().Hex,
	}
	theme.InfoColor = ThemeColor{
		Dark:  mocha.Blue().Hex,
		Light: latte.Blue().Hex,
	}

	// Text colors
	theme.TextColor = ThemeColor{
		Dark:  mocha.Text().Hex,
		Light: latte.Text().Hex,
	}
	theme.TextMutedColor = ThemeColor{
		Dark:  mocha.Subtext0().Hex,
		Light: latte.Subtext0().Hex,
	}
	theme.TextEmphasizedColor = ThemeColor{
		Dark:  mocha.Lavender().Hex,
		Light: latte.Lavender().Hex,
	}

	// Background colors
	theme.BackgroundColor = ThemeColor{
		Dark:  "#212121", // From existing styles
		Light: "#EEEEEE", // Light equivalent
	}
	theme.BackgroundSecondaryColor = ThemeColor{
		Dark:  "#2c2c2c", // From existing styles
		Light: "#E0E0E0", // Light equivalent
	}
	theme.BackgroundDarkerColor = ThemeColor{
		Dark:  "#181818", // From existing styles
		Light: "#F5F5F5", // Light equivalent
	}

	// Border colors
	theme.BorderNormalColor = ThemeColor{
		Dark:  "#4b4c5c", // From existing styles
		Light: "#BDBDBD", // Light equivalent
	}
	theme.BorderFocusedColor = ThemeColor{
		Dark:  mocha.Blue().Hex,
		Light: latte.Blue().Hex,
	}
	theme.BorderDimColor = ThemeColor{
		Dark:  mocha.Surface0().Hex,
		Light: latte.Surface0().Hex,
	}

	// Diff view colors
	theme.DiffAddedColor = ThemeColor{
		Dark:  "#478247", // From existing diff.go
		Light: "#2E7D32", // Light equivalent
	}
	theme.DiffRemovedColor = ThemeColor{
		Dark:  "#7C4444", // From existing diff.go
		Light: "#C62828", // Light equivalent
	}
	theme.DiffContextColor = ThemeColor{
		Dark:  "#a0a0a0", // From existing diff.go
		Light: "#757575", // Light equivalent
	}
	theme.DiffHunkHeaderColor = ThemeColor{
		Dark:  "#a0a0a0", // From existing diff.go
		Light: "#757575", // Light equivalent
	}
	theme.DiffHighlightAddedColor = ThemeColor{
		Dark:  "#DAFADA", // From existing diff.go
		Light: "#A5D6A7", // Light equivalent
	}
	theme.DiffHighlightRemovedColor = ThemeColor{
		Dark:  "#FADADD", // From existing diff.go
		Light: "#EF9A9A", // Light equivalent
	}
	theme.DiffAddedBgColor = ThemeColor{
		Dark:  "#303A30", // From existing diff.go
		Light: "#E8F5E9", // Light equivalent
	}
	theme.DiffRemovedBgColor = ThemeColor{
		Dark:  "#3A3030", // From existing diff.go
		Light: "#FFEBEE", // Light equivalent
	}
	theme.DiffContextBgColor = ThemeColor{
		Dark:  "#212121", // From existing diff.go
		Light: "#F5F5F5", // Light equivalent
	}
	theme.DiffLineNumberColor = ThemeColor{
		Dark:  "#888888", // From existing diff.go
		Light: "#9E9E9E", // Light equivalent
	}
	theme.DiffAddedLineNumberBgColor = ThemeColor{
		Dark:  "#293229", // From existing diff.go
		Light: "#C8E6C9", // Light equivalent
	}
	theme.DiffRemovedLineNumberBgColor = ThemeColor{
		Dark:  "#332929", // From existing diff.go
		Light: "#FFCDD2", // Light equivalent
	}

	// Markdown colors
	theme.MarkdownTextColor = ThemeColor{
		Dark:  mocha.Text().Hex,
		Light: latte.Text().Hex,
	}
	theme.MarkdownHeadingColor = ThemeColor{
		Dark:  mocha.Mauve().Hex,
		Light: latte.Mauve().Hex,
	}
	theme.MarkdownLinkColor = ThemeColor{
		Dark:  mocha.Sky().Hex,
		Light: latte.Sky().Hex,
	}
	theme.MarkdownLinkTextColor = ThemeColor{
		Dark:  mocha.Pink().Hex,
		Light: latte.Pink().Hex,
	}
	theme.MarkdownCodeColor = ThemeColor{
		Dark:  mocha.Green().Hex,
		Light: latte.Green().Hex,
	}
	theme.MarkdownBlockQuoteColor = ThemeColor{
		Dark:  mocha.Yellow().Hex,
		Light: latte.Yellow().Hex,
	}
	theme.MarkdownEmphColor = ThemeColor{
		Dark:  mocha.Yellow().Hex,
		Light: latte.Yellow().Hex,
	}
	theme.MarkdownStrongColor = ThemeColor{
		Dark:  mocha.Peach().Hex,
		Light: latte.Peach().Hex,
	}
	theme.MarkdownHorizontalRuleColor = ThemeColor{
		Dark:  mocha.Overlay0().Hex,
		Light: latte.Overlay0().Hex,
	}
	theme.MarkdownListItemColor = ThemeColor{
		Dark:  mocha.Blue().Hex,
		Light: latte.Blue().Hex,
	}
	theme.MarkdownListEnumerationColor = ThemeColor{
		Dark:  mocha.Sky().Hex,
		Light: latte.Sky().Hex,
	}
	theme.MarkdownImageColor = ThemeColor{
		Dark:  mocha.Sapphire().Hex,
		Light: latte.Sapphire().Hex,
	}
	theme.MarkdownImageTextColor = ThemeColor{
		Dark:  mocha.Pink().Hex,
		Light: latte.Pink().Hex,
	}
	theme.MarkdownCodeBlockColor = ThemeColor{
		Dark:  mocha.Text().Hex,
		Light: latte.Text().Hex,
	}

	// Syntax highlighting colors
	theme.SyntaxCommentColor = ThemeColor{
		Dark:  mocha.Overlay1().Hex,
		Light: latte.Overlay1().Hex,
	}
	theme.SyntaxKeywordColor = ThemeColor{
		Dark:  mocha.Pink().Hex,
		Light: latte.Pink().Hex,
	}
	theme.SyntaxFunctionColor = ThemeColor{
		Dark:  mocha.Green().Hex,
		Light: latte.Green().Hex,
	}
	theme.SyntaxVariableColor = ThemeColor{
		Dark:  mocha.Sky().Hex,
		Light: latte.Sky().Hex,
	}
	theme.SyntaxStringColor = ThemeColor{
		Dark:  mocha.Yellow().Hex,
		Light: latte.Yellow().Hex,
	}
	theme.SyntaxNumberColor = ThemeColor{
		Dark:  mocha.Teal().Hex,
		Light: latte.Teal().Hex,
	}
	theme.SyntaxTypeColor = ThemeColor{
		Dark:  mocha.Sky().Hex,
		Light: latte.Sky().Hex,
	}
	theme.SyntaxOperatorColor = ThemeColor{
		Dark:  mocha.Pink().Hex,
		Light: latte.Pink().Hex,
	}
	theme.SyntaxPunctuationColor = ThemeColor{
		Dark:  mocha.Text().Hex,
		Light: latte.Text().Hex,
	}

	return theme
}

func init() {
	// Register the Catppuccin theme with the theme manager
	RegisterTheme("catppuccin", NewCatppuccinTheme())
}
