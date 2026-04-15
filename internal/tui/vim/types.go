package vim

// VimMode represents the current editing mode.
type VimMode string

const (
	ModeInsert VimMode = "INSERT"
	ModeNormal VimMode = "NORMAL"
)

// Operator represents a vim operator (d, c, y).
type Operator string

const (
	OpDelete Operator = "delete"
	OpChange Operator = "change"
	OpYank   Operator = "yank"
)

// FindType represents a find motion type.
type FindType rune

const (
	FindF FindType = 'f'
	FindB FindType = 'F'
	FindT FindType = 't'
	FindR FindType = 'T'
)

// TextObjScope represents inner vs around text objects.
type TextObjScope string

const (
	ScopeInner  TextObjScope = "inner"
	ScopeAround TextObjScope = "around"
)

// VimState is the top-level state.
type VimState struct {
	Mode         VimMode
	InsertedText string       // tracked in INSERT mode for dot-repeat
	Command      CommandState // active in NORMAL mode
}

// CommandState represents the NORMAL mode sub-state machine.
// Go lacks discriminated unions, so we use a sealed interface.
type CommandState interface {
	commandState() // sealed marker method
}

type CommandIdle struct{}
type CommandCount struct{ Digits string }
type CommandOperator struct {
	Op    Operator
	Count int
}
type CommandOperatorCount struct {
	Op     Operator
	Count  int
	Digits string
}
type CommandOperatorFind struct {
	Op    Operator
	Count int
	Find  FindType
}
type CommandOperatorTextObj struct {
	Op    Operator
	Count int
	Scope TextObjScope
}
type CommandFind struct {
	Find  FindType
	Count int
}
type CommandG struct{ Count int }
type CommandOperatorG struct {
	Op    Operator
	Count int
}
type CommandReplace struct{ Count int }
type CommandIndent struct {
	Dir   rune
	Count int
}

func (CommandIdle) commandState()            {}
func (CommandCount) commandState()           {}
func (CommandOperator) commandState()        {}
func (CommandOperatorCount) commandState()   {}
func (CommandOperatorFind) commandState()    {}
func (CommandOperatorTextObj) commandState() {}
func (CommandFind) commandState()            {}
func (CommandG) commandState()               {}
func (CommandOperatorG) commandState()       {}
func (CommandReplace) commandState()         {}
func (CommandIndent) commandState()          {}

// FindRecord stores the last find command for repeat.
type FindRecord struct {
	Type FindType
	Char string
}

// PersistentState survives across commands.
type PersistentState struct {
	LastChange *RecordedChange
	LastFind   *FindRecord
	Register   string
	Linewise   bool
}

// RecordedChange captures a change for dot-repeat.
// Go lacks discriminated unions, so we use a type tag + fields.
type RecordedChange struct {
	Type      string // "insert", "operator", "operatorTextObj", "operatorFind", "replace", "x", "toggleCase", "indent", "openLine", "join"
	Text      string // for insert
	Op        Operator
	Motion    string
	Count     int
	ObjType   string
	Scope     TextObjScope
	Find      FindType
	Char      string
	Dir       rune   // for indent
	Direction string // for openLine: "above" or "below"
}

// Key group helpers

var operatorKeys = map[string]Operator{
	"d": OpDelete,
	"c": OpChange,
	"y": OpYank,
}

func IsOperatorKey(key string) (Operator, bool) {
	op, ok := operatorKeys[key]
	return op, ok
}

var simpleMotions = map[string]bool{
	"h": true, "l": true, "j": true, "k": true,
	"w": true, "b": true, "e": true,
	"W": true, "B": true, "E": true,
	"0": true, "^": true, "$": true,
}

func IsSimpleMotion(key string) bool {
	return simpleMotions[key]
}

var findKeys = map[string]FindType{
	"f": FindF,
	"F": FindB,
	"t": FindT,
	"T": FindR,
}

func IsFindKey(key string) (FindType, bool) {
	ft, ok := findKeys[key]
	return ft, ok
}

var textObjScopes = map[string]TextObjScope{
	"i": ScopeInner,
	"a": ScopeAround,
}

func IsTextObjScopeKey(key string) (TextObjScope, bool) {
	s, ok := textObjScopes[key]
	return s, ok
}

var textObjTypes = map[string]bool{
	"w": true, "W": true,
	"\"": true, "'": true, "`": true,
	"(": true, ")": true, "b": true,
	"[": true, "]": true,
	"{": true, "}": true, "B": true,
	"<": true, ">": true,
}

func IsTextObjType(key string) bool {
	return textObjTypes[key]
}

const MaxVimCount = 10000

func CreateInitialVimState() VimState {
	return VimState{Mode: ModeInsert, InsertedText: ""}
}

func CreateInitialPersistentState() PersistentState {
	return PersistentState{}
}
