package theme

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// ThemeColor stores both dark and light hex color values and resolves
// to the appropriate color based on the terminal background.
type ThemeColor struct {
	Dark  string
	Light string
}

var isDarkBG = true

// SetIsDark sets whether the terminal background is dark.
func SetIsDark(dark bool) { isDarkBG = dark }

// IsDark returns whether the terminal background is dark.
func IsDark() bool { return isDarkBG }

// Color resolves to the appropriate color based on terminal background.
func (tc ThemeColor) Color() color.Color {
	if isDarkBG {
		return lipgloss.Color(tc.Dark)
	}
	return lipgloss.Color(tc.Light)
}

// Theme defines the interface for all UI themes in the application.
type Theme interface {
	Primary() color.Color
	Secondary() color.Color
	Accent() color.Color

	Error() color.Color
	Warning() color.Color
	Success() color.Color
	Info() color.Color

	Text() color.Color
	TextMuted() color.Color
	TextEmphasized() color.Color

	Background() color.Color
	BackgroundSecondary() color.Color
	BackgroundDarker() color.Color

	BorderNormal() color.Color
	BorderFocused() color.Color
	BorderDim() color.Color

	DiffAdded() color.Color
	DiffRemoved() color.Color
	DiffContext() color.Color
	DiffHunkHeader() color.Color
	DiffHighlightAdded() color.Color
	DiffHighlightRemoved() color.Color
	DiffAddedBg() color.Color
	DiffRemovedBg() color.Color
	DiffContextBg() color.Color
	DiffLineNumber() color.Color
	DiffAddedLineNumberBg() color.Color
	DiffRemovedLineNumberBg() color.Color

	MarkdownText() color.Color
	MarkdownHeading() color.Color
	MarkdownLink() color.Color
	MarkdownLinkText() color.Color
	MarkdownCode() color.Color
	MarkdownBlockQuote() color.Color
	MarkdownEmph() color.Color
	MarkdownStrong() color.Color
	MarkdownHorizontalRule() color.Color
	MarkdownListItem() color.Color
	MarkdownListEnumeration() color.Color
	MarkdownImage() color.Color
	MarkdownImageText() color.Color
	MarkdownCodeBlock() color.Color

	SyntaxComment() color.Color
	SyntaxKeyword() color.Color
	SyntaxFunction() color.Color
	SyntaxVariable() color.Color
	SyntaxString() color.Color
	SyntaxNumber() color.Color
	SyntaxType() color.Color
	SyntaxOperator() color.Color
	SyntaxPunctuation() color.Color
}

// BaseTheme provides a default implementation of the Theme interface
// that can be embedded in concrete theme implementations.
type BaseTheme struct {
	PrimaryColor   ThemeColor
	SecondaryColor ThemeColor
	AccentColor    ThemeColor

	ErrorColor   ThemeColor
	WarningColor ThemeColor
	SuccessColor ThemeColor
	InfoColor    ThemeColor

	TextColor           ThemeColor
	TextMutedColor      ThemeColor
	TextEmphasizedColor ThemeColor

	BackgroundColor          ThemeColor
	BackgroundSecondaryColor ThemeColor
	BackgroundDarkerColor    ThemeColor

	BorderNormalColor  ThemeColor
	BorderFocusedColor ThemeColor
	BorderDimColor     ThemeColor

	DiffAddedColor               ThemeColor
	DiffRemovedColor             ThemeColor
	DiffContextColor             ThemeColor
	DiffHunkHeaderColor          ThemeColor
	DiffHighlightAddedColor      ThemeColor
	DiffHighlightRemovedColor    ThemeColor
	DiffAddedBgColor             ThemeColor
	DiffRemovedBgColor           ThemeColor
	DiffContextBgColor           ThemeColor
	DiffLineNumberColor          ThemeColor
	DiffAddedLineNumberBgColor   ThemeColor
	DiffRemovedLineNumberBgColor ThemeColor

	MarkdownTextColor            ThemeColor
	MarkdownHeadingColor         ThemeColor
	MarkdownLinkColor            ThemeColor
	MarkdownLinkTextColor        ThemeColor
	MarkdownCodeColor            ThemeColor
	MarkdownBlockQuoteColor      ThemeColor
	MarkdownEmphColor            ThemeColor
	MarkdownStrongColor          ThemeColor
	MarkdownHorizontalRuleColor  ThemeColor
	MarkdownListItemColor        ThemeColor
	MarkdownListEnumerationColor ThemeColor
	MarkdownImageColor           ThemeColor
	MarkdownImageTextColor       ThemeColor
	MarkdownCodeBlockColor       ThemeColor

	SyntaxCommentColor     ThemeColor
	SyntaxKeywordColor     ThemeColor
	SyntaxFunctionColor    ThemeColor
	SyntaxVariableColor    ThemeColor
	SyntaxStringColor      ThemeColor
	SyntaxNumberColor      ThemeColor
	SyntaxTypeColor        ThemeColor
	SyntaxOperatorColor    ThemeColor
	SyntaxPunctuationColor ThemeColor
}

func (t *BaseTheme) Primary() color.Color   { return t.PrimaryColor.Color() }
func (t *BaseTheme) Secondary() color.Color { return t.SecondaryColor.Color() }
func (t *BaseTheme) Accent() color.Color    { return t.AccentColor.Color() }

func (t *BaseTheme) Error() color.Color   { return t.ErrorColor.Color() }
func (t *BaseTheme) Warning() color.Color { return t.WarningColor.Color() }
func (t *BaseTheme) Success() color.Color { return t.SuccessColor.Color() }
func (t *BaseTheme) Info() color.Color    { return t.InfoColor.Color() }

func (t *BaseTheme) Text() color.Color           { return t.TextColor.Color() }
func (t *BaseTheme) TextMuted() color.Color      { return t.TextMutedColor.Color() }
func (t *BaseTheme) TextEmphasized() color.Color { return t.TextEmphasizedColor.Color() }

func (t *BaseTheme) Background() color.Color          { return t.BackgroundColor.Color() }
func (t *BaseTheme) BackgroundSecondary() color.Color { return t.BackgroundSecondaryColor.Color() }
func (t *BaseTheme) BackgroundDarker() color.Color    { return t.BackgroundDarkerColor.Color() }

func (t *BaseTheme) BorderNormal() color.Color  { return t.BorderNormalColor.Color() }
func (t *BaseTheme) BorderFocused() color.Color { return t.BorderFocusedColor.Color() }
func (t *BaseTheme) BorderDim() color.Color     { return t.BorderDimColor.Color() }

func (t *BaseTheme) DiffAdded() color.Color            { return t.DiffAddedColor.Color() }
func (t *BaseTheme) DiffRemoved() color.Color          { return t.DiffRemovedColor.Color() }
func (t *BaseTheme) DiffContext() color.Color          { return t.DiffContextColor.Color() }
func (t *BaseTheme) DiffHunkHeader() color.Color       { return t.DiffHunkHeaderColor.Color() }
func (t *BaseTheme) DiffHighlightAdded() color.Color   { return t.DiffHighlightAddedColor.Color() }
func (t *BaseTheme) DiffHighlightRemoved() color.Color { return t.DiffHighlightRemovedColor.Color() }
func (t *BaseTheme) DiffAddedBg() color.Color          { return t.DiffAddedBgColor.Color() }
func (t *BaseTheme) DiffRemovedBg() color.Color        { return t.DiffRemovedBgColor.Color() }
func (t *BaseTheme) DiffContextBg() color.Color        { return t.DiffContextBgColor.Color() }
func (t *BaseTheme) DiffLineNumber() color.Color       { return t.DiffLineNumberColor.Color() }
func (t *BaseTheme) DiffAddedLineNumberBg() color.Color {
	return t.DiffAddedLineNumberBgColor.Color()
}
func (t *BaseTheme) DiffRemovedLineNumberBg() color.Color {
	return t.DiffRemovedLineNumberBgColor.Color()
}

func (t *BaseTheme) MarkdownText() color.Color       { return t.MarkdownTextColor.Color() }
func (t *BaseTheme) MarkdownHeading() color.Color    { return t.MarkdownHeadingColor.Color() }
func (t *BaseTheme) MarkdownLink() color.Color       { return t.MarkdownLinkColor.Color() }
func (t *BaseTheme) MarkdownLinkText() color.Color   { return t.MarkdownLinkTextColor.Color() }
func (t *BaseTheme) MarkdownCode() color.Color       { return t.MarkdownCodeColor.Color() }
func (t *BaseTheme) MarkdownBlockQuote() color.Color { return t.MarkdownBlockQuoteColor.Color() }
func (t *BaseTheme) MarkdownEmph() color.Color       { return t.MarkdownEmphColor.Color() }
func (t *BaseTheme) MarkdownStrong() color.Color     { return t.MarkdownStrongColor.Color() }
func (t *BaseTheme) MarkdownHorizontalRule() color.Color {
	return t.MarkdownHorizontalRuleColor.Color()
}
func (t *BaseTheme) MarkdownListItem() color.Color { return t.MarkdownListItemColor.Color() }
func (t *BaseTheme) MarkdownListEnumeration() color.Color {
	return t.MarkdownListEnumerationColor.Color()
}
func (t *BaseTheme) MarkdownImage() color.Color     { return t.MarkdownImageColor.Color() }
func (t *BaseTheme) MarkdownImageText() color.Color { return t.MarkdownImageTextColor.Color() }
func (t *BaseTheme) MarkdownCodeBlock() color.Color { return t.MarkdownCodeBlockColor.Color() }

func (t *BaseTheme) SyntaxComment() color.Color     { return t.SyntaxCommentColor.Color() }
func (t *BaseTheme) SyntaxKeyword() color.Color     { return t.SyntaxKeywordColor.Color() }
func (t *BaseTheme) SyntaxFunction() color.Color    { return t.SyntaxFunctionColor.Color() }
func (t *BaseTheme) SyntaxVariable() color.Color    { return t.SyntaxVariableColor.Color() }
func (t *BaseTheme) SyntaxString() color.Color      { return t.SyntaxStringColor.Color() }
func (t *BaseTheme) SyntaxNumber() color.Color      { return t.SyntaxNumberColor.Color() }
func (t *BaseTheme) SyntaxType() color.Color        { return t.SyntaxTypeColor.Color() }
func (t *BaseTheme) SyntaxOperator() color.Color    { return t.SyntaxOperatorColor.Color() }
func (t *BaseTheme) SyntaxPunctuation() color.Color { return t.SyntaxPunctuationColor.Color() }
