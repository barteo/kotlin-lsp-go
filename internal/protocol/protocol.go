// Package protocol holds the Language Server Protocol 3.17 types that
// kcls uses, and a Mapper between LSP positions and byte offsets.
//
// Only the parts of the protocol kcls serves are declared; fields a
// client sends that are not declared here are ignored when decoding.
package protocol

import "encoding/json"

// DocumentURI is a document's URI, as file:///a/b.kt.
type DocumentURI string

// Position is a zero-based line and a character offset in the line, in
// the units of the negotiated position encoding.
type Position struct {
	Line      uint32 `json:"line"`
	Character uint32 `json:"character"`
}

// Range is a half-open range of positions.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is a range in a document.
type Location struct {
	URI   DocumentURI `json:"uri"`
	Range Range       `json:"range"`
}

// The position encodings.
const (
	UTF8  = "utf-8"
	UTF16 = "utf-16"
	UTF32 = "utf-32"
)

// ---- lifecycle ----

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type WorkspaceFolder struct {
	URI  DocumentURI `json:"uri"`
	Name string      `json:"name"`
}

type InitializeParams struct {
	ProcessID             *int               `json:"processId"`
	ClientInfo            *ClientInfo        `json:"clientInfo,omitempty"`
	RootPath              *string            `json:"rootPath,omitempty"`
	RootURI               *DocumentURI       `json:"rootUri"`
	InitializationOptions json.RawMessage    `json:"initializationOptions,omitempty"`
	Capabilities          ClientCapabilities `json:"capabilities"`
	Trace                 string             `json:"trace,omitempty"`
	WorkspaceFolders      []WorkspaceFolder  `json:"workspaceFolders,omitempty"`
}

type ClientCapabilities struct {
	Workspace    *WorkspaceClientCapabilities    `json:"workspace,omitempty"`
	TextDocument *TextDocumentClientCapabilities `json:"textDocument,omitempty"`
	Window       *WindowClientCapabilities       `json:"window,omitempty"`
	General      *GeneralClientCapabilities      `json:"general,omitempty"`
}

type GeneralClientCapabilities struct {
	PositionEncodings []string `json:"positionEncodings,omitempty"`
}

type WindowClientCapabilities struct {
	WorkDoneProgress bool `json:"workDoneProgress,omitempty"`
}

type DynamicRegistration struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type WorkspaceClientCapabilities struct {
	DidChangeWatchedFiles *DynamicRegistration `json:"didChangeWatchedFiles,omitempty"`
	WorkspaceFolders      bool                 `json:"workspaceFolders,omitempty"`
	Configuration         bool                 `json:"configuration,omitempty"`
	Diagnostics           *RefreshCapability   `json:"diagnostics,omitempty"`
	SemanticTokens        *RefreshCapability   `json:"semanticTokens,omitempty"`
	InlayHint             *RefreshCapability   `json:"inlayHint,omitempty"`
}

type RefreshCapability struct {
	RefreshSupport bool `json:"refreshSupport,omitempty"`
}

type TextDocumentClientCapabilities struct {
	DocumentSymbol *DocumentSymbolClientCapabilities `json:"documentSymbol,omitempty"`
	FoldingRange   *FoldingRangeClientCapabilities   `json:"foldingRange,omitempty"`
	Diagnostic     *DynamicRegistration              `json:"diagnostic,omitempty"`
	SemanticTokens *SemanticTokensClientCapabilities `json:"semanticTokens,omitempty"`
	Hover          *HoverClientCapabilities          `json:"hover,omitempty"`
	Definition     *LinkClientCapabilities           `json:"definition,omitempty"`
	TypeDefinition *LinkClientCapabilities           `json:"typeDefinition,omitempty"`
	Implementation *LinkClientCapabilities           `json:"implementation,omitempty"`
	Completion     *CompletionClientCapabilities     `json:"completion,omitempty"`
	SignatureHelp  *SignatureHelpClientCapabilities  `json:"signatureHelp,omitempty"`
}

type CompletionClientCapabilities struct {
	CompletionItem *CompletionItemClientCapabilities `json:"completionItem,omitempty"`
}

type CompletionItemClientCapabilities struct {
	SnippetSupport      bool               `json:"snippetSupport,omitempty"`
	DeprecatedSupport   bool               `json:"deprecatedSupport,omitempty"`
	TagSupport          *ValueSet          `json:"tagSupport,omitempty"`
	LabelDetailsSupport bool               `json:"labelDetailsSupport,omitempty"`
	ResolveSupport      *PropertiesSupport `json:"resolveSupport,omitempty"`
}

type ValueSet struct {
	ValueSet []int `json:"valueSet"`
}

type PropertiesSupport struct {
	Properties []string `json:"properties"`
}

type SignatureHelpClientCapabilities struct {
	SignatureInformation *SignatureInformationClientCapabilities `json:"signatureInformation,omitempty"`
}

type SignatureInformationClientCapabilities struct {
	ParameterInformation   *ParameterInformationClientCapabilities `json:"parameterInformation,omitempty"`
	ActiveParameterSupport bool                                    `json:"activeParameterSupport,omitempty"`
}

type ParameterInformationClientCapabilities struct {
	LabelOffsetSupport bool `json:"labelOffsetSupport,omitempty"`
}

type HoverClientCapabilities struct {
	ContentFormat []string `json:"contentFormat,omitempty"`
}

type LinkClientCapabilities struct {
	LinkSupport bool `json:"linkSupport,omitempty"`
}

type DocumentSymbolClientCapabilities struct {
	HierarchicalDocumentSymbolSupport bool `json:"hierarchicalDocumentSymbolSupport,omitempty"`
}

type FoldingRangeClientCapabilities struct {
	LineFoldingOnly bool `json:"lineFoldingOnly,omitempty"`
}

type SemanticTokensClientCapabilities struct {
	MultilineTokenSupport bool `json:"multilineTokenSupport,omitempty"`
}

type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   *ServerInfo        `json:"serverInfo,omitempty"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// TextDocumentSyncKind says how a client sends document changes.
type TextDocumentSyncKind int

const (
	SyncNone        TextDocumentSyncKind = 0
	SyncFull        TextDocumentSyncKind = 1
	SyncIncremental TextDocumentSyncKind = 2
)

type SaveOptions struct {
	IncludeText bool `json:"includeText,omitempty"`
}

type TextDocumentSyncOptions struct {
	OpenClose bool                 `json:"openClose"`
	Change    TextDocumentSyncKind `json:"change"`
	Save      *SaveOptions         `json:"save,omitempty"`
}

type ServerCapabilities struct {
	PositionEncoding          string                   `json:"positionEncoding,omitempty"`
	TextDocumentSync          *TextDocumentSyncOptions `json:"textDocumentSync,omitempty"`
	DocumentSymbolProvider    bool                     `json:"documentSymbolProvider,omitempty"`
	FoldingRangeProvider      bool                     `json:"foldingRangeProvider,omitempty"`
	SelectionRangeProvider    bool                     `json:"selectionRangeProvider,omitempty"`
	SemanticTokensProvider    *SemanticTokensOptions   `json:"semanticTokensProvider,omitempty"`
	DiagnosticProvider        *DiagnosticOptions       `json:"diagnosticProvider,omitempty"`
	HoverProvider             bool                     `json:"hoverProvider,omitempty"`
	DefinitionProvider        bool                     `json:"definitionProvider,omitempty"`
	TypeDefinitionProvider    bool                     `json:"typeDefinitionProvider,omitempty"`
	DocumentHighlightProvider bool                     `json:"documentHighlightProvider,omitempty"`
	CompletionProvider        *CompletionOptions       `json:"completionProvider,omitempty"`
	SignatureHelpProvider     *SignatureHelpOptions    `json:"signatureHelpProvider,omitempty"`
	ReferencesProvider        bool                     `json:"referencesProvider,omitempty"`
	ImplementationProvider    bool                     `json:"implementationProvider,omitempty"`
	RenameProvider            *RenameOptions           `json:"renameProvider,omitempty"`
	CallHierarchyProvider     bool                     `json:"callHierarchyProvider,omitempty"`
	TypeHierarchyProvider     bool                     `json:"typeHierarchyProvider,omitempty"`
	WorkspaceSymbolProvider   bool                     `json:"workspaceSymbolProvider,omitempty"`
	ExecuteCommandProvider    *ExecuteCommandOptions   `json:"executeCommandProvider,omitempty"`
	InlayHintProvider         bool                     `json:"inlayHintProvider,omitempty"`
	CodeActionProvider        *CodeActionOptions       `json:"codeActionProvider,omitempty"`
	Workspace                 *WorkspaceOptions        `json:"workspace,omitempty"`
}

type ExecuteCommandOptions struct {
	Commands []string `json:"commands"`
}

type WorkspaceOptions struct {
	WorkspaceFolders *WorkspaceFoldersServerCapabilities `json:"workspaceFolders,omitempty"`
}

type WorkspaceFoldersServerCapabilities struct {
	Supported           bool `json:"supported,omitempty"`
	ChangeNotifications bool `json:"changeNotifications,omitempty"`
}

type DiagnosticOptions struct {
	Identifier            string `json:"identifier,omitempty"`
	InterFileDependencies bool   `json:"interFileDependencies"`
	WorkspaceDiagnostics  bool   `json:"workspaceDiagnostics"`
}

type InitializedParams struct{}

// ---- registration ----

type Registration struct {
	ID              string `json:"id"`
	Method          string `json:"method"`
	RegisterOptions any    `json:"registerOptions,omitempty"`
}

type RegistrationParams struct {
	Registrations []Registration `json:"registrations"`
}

type FileSystemWatcher struct {
	GlobPattern string `json:"globPattern"`
	Kind        int    `json:"kind,omitempty"`
}

type DidChangeWatchedFilesRegistrationOptions struct {
	Watchers []FileSystemWatcher `json:"watchers"`
}

// ---- documents ----

type TextDocumentIdentifier struct {
	URI DocumentURI `json:"uri"`
}

type VersionedTextDocumentIdentifier struct {
	URI     DocumentURI `json:"uri"`
	Version int32       `json:"version"`
}

type TextDocumentItem struct {
	URI        DocumentURI `json:"uri"`
	LanguageID string      `json:"languageId"`
	Version    int32       `json:"version"`
	Text       string      `json:"text"`
}

type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// TextDocumentContentChangeEvent replaces Range with Text, or the whole
// document if Range is nil.
type TextDocumentContentChangeEvent struct {
	Range *Range `json:"range,omitempty"`
	Text  string `json:"text"`
}

type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent `json:"contentChanges"`
}

type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type DidSaveTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Text         *string                `json:"text,omitempty"`
}

// FileChangeType is a FileEvent's kind of change.
type FileChangeType int

const (
	FileCreated FileChangeType = 1
	FileChanged FileChangeType = 2
	FileDeleted FileChangeType = 3
)

type FileEvent struct {
	URI  DocumentURI    `json:"uri"`
	Type FileChangeType `json:"type"`
}

type DidChangeWatchedFilesParams struct {
	Changes []FileEvent `json:"changes"`
}

// ---- diagnostics ----

type DiagnosticSeverity int

const (
	SeverityError       DiagnosticSeverity = 1
	SeverityWarning     DiagnosticSeverity = 2
	SeverityInformation DiagnosticSeverity = 3
	SeverityHint        DiagnosticSeverity = 4
)

type DiagnosticTag int

const (
	TagUnnecessary DiagnosticTag = 1
	TagDeprecated  DiagnosticTag = 2
)

type Diagnostic struct {
	Range    Range              `json:"range"`
	Severity DiagnosticSeverity `json:"severity,omitempty"`
	Code     string             `json:"code,omitempty"`
	Source   string             `json:"source,omitempty"`
	Message  string             `json:"message"`
	Tags     []DiagnosticTag    `json:"tags,omitempty"`
}

type PublishDiagnosticsParams struct {
	URI         DocumentURI  `json:"uri"`
	Version     *int32       `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type DocumentDiagnosticParams struct {
	TextDocument     TextDocumentIdentifier `json:"textDocument"`
	Identifier       string                 `json:"identifier,omitempty"`
	PreviousResultID string                 `json:"previousResultId,omitempty"`
}

// DocumentDiagnosticReport is a full report (Kind "full") or, when
// nothing changed since PreviousResultID, an unchanged one.
type DocumentDiagnosticReport struct {
	Kind     string       `json:"kind"`
	ResultID string       `json:"resultId,omitempty"`
	Items    []Diagnostic `json:"items"`
}

// ---- symbols ----

type SymbolKind int

const (
	SymbolFile          SymbolKind = 1
	SymbolModule        SymbolKind = 2
	SymbolNamespace     SymbolKind = 3
	SymbolPackage       SymbolKind = 4
	SymbolClass         SymbolKind = 5
	SymbolMethod        SymbolKind = 6
	SymbolProperty      SymbolKind = 7
	SymbolField         SymbolKind = 8
	SymbolConstructor   SymbolKind = 9
	SymbolEnum          SymbolKind = 10
	SymbolInterface     SymbolKind = 11
	SymbolFunction      SymbolKind = 12
	SymbolVariable      SymbolKind = 13
	SymbolConstant      SymbolKind = 14
	SymbolString        SymbolKind = 15
	SymbolNumber        SymbolKind = 16
	SymbolBoolean       SymbolKind = 17
	SymbolArray         SymbolKind = 18
	SymbolObject        SymbolKind = 19
	SymbolKey           SymbolKind = 20
	SymbolNull          SymbolKind = 21
	SymbolEnumMember    SymbolKind = 22
	SymbolStruct        SymbolKind = 23
	SymbolEvent         SymbolKind = 24
	SymbolOperator      SymbolKind = 25
	SymbolTypeParameter SymbolKind = 26
)

type SymbolTag int

const SymbolTagDeprecated SymbolTag = 1

type DocumentSymbolParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           SymbolKind       `json:"kind"`
	Tags           []SymbolTag      `json:"tags,omitempty"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// SymbolInformation is the flat form of a symbol, for clients without
// hierarchical document symbols.
type SymbolInformation struct {
	Name          string      `json:"name"`
	Kind          SymbolKind  `json:"kind"`
	Tags          []SymbolTag `json:"tags,omitempty"`
	Location      Location    `json:"location"`
	ContainerName string      `json:"containerName,omitempty"`
}

// ---- folding and selection ----

type FoldingRangeParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

const (
	FoldingComment = "comment"
	FoldingImports = "imports"
	FoldingRegion  = "region"
)

type FoldingRange struct {
	StartLine      uint32  `json:"startLine"`
	StartCharacter *uint32 `json:"startCharacter,omitempty"`
	EndLine        uint32  `json:"endLine"`
	EndCharacter   *uint32 `json:"endCharacter,omitempty"`
	Kind           string  `json:"kind,omitempty"`
	CollapsedText  string  `json:"collapsedText,omitempty"`
}

type SelectionRangeParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Positions    []Position             `json:"positions"`
}

type SelectionRange struct {
	Range  Range           `json:"range"`
	Parent *SelectionRange `json:"parent,omitempty"`
}

// ---- semantic tokens ----

type SemanticTokensLegend struct {
	TokenTypes     []string `json:"tokenTypes"`
	TokenModifiers []string `json:"tokenModifiers"`
}

type SemanticTokensOptions struct {
	Legend SemanticTokensLegend `json:"legend"`
	Range  bool                 `json:"range,omitempty"`
	Full   bool                 `json:"full,omitempty"`
}

type SemanticTokensParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type SemanticTokensRangeParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
}

type SemanticTokens struct {
	ResultID string   `json:"resultId,omitempty"`
	Data     []uint32 `json:"data"`
}

// ---- window ----

type MessageType int

const (
	MessageError   MessageType = 1
	MessageWarning MessageType = 2
	MessageInfo    MessageType = 3
	MessageLog     MessageType = 4
)

type LogMessageParams struct {
	Type    MessageType `json:"type"`
	Message string      `json:"message"`
}

// ---- language features ----

// MarkupContent is text for the client to render: "plaintext" or
// "markdown".
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type HoverParams = TextDocumentPositionParams

type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

type DefinitionParams = TextDocumentPositionParams

// LocationLink is a definition: where it is, its whole range, and the
// range to select, its name.
type LocationLink struct {
	OriginSelectionRange *Range      `json:"originSelectionRange,omitempty"`
	TargetURI            DocumentURI `json:"targetUri"`
	TargetRange          Range       `json:"targetRange"`
	TargetSelectionRange Range       `json:"targetSelectionRange"`
}

type DocumentHighlightKind int

const (
	HighlightText  DocumentHighlightKind = 1
	HighlightRead  DocumentHighlightKind = 2
	HighlightWrite DocumentHighlightKind = 3
)

type DocumentHighlight struct {
	Range Range                 `json:"range"`
	Kind  DocumentHighlightKind `json:"kind,omitempty"`
}

// ---- commands and progress ----

type ExecuteCommandParams struct {
	Command   string            `json:"command"`
	Arguments []json.RawMessage `json:"arguments,omitempty"`
}

type ShowMessageParams struct {
	Type    MessageType `json:"type"`
	Message string      `json:"message"`
}

type WorkDoneProgressCreateParams struct {
	Token string `json:"token"`
}

type ProgressParams struct {
	Token string `json:"token"`
	Value any    `json:"value"`
}

type WorkDoneProgressBegin struct {
	Kind        string `json:"kind"` // "begin"
	Title       string `json:"title"`
	Cancellable bool   `json:"cancellable"`
	Message     string `json:"message,omitempty"`
}

type WorkDoneProgressEnd struct {
	Kind    string `json:"kind"` // "end"
	Message string `json:"message,omitempty"`
}

type WorkspaceFoldersChangeEvent struct {
	Added   []WorkspaceFolder `json:"added"`
	Removed []WorkspaceFolder `json:"removed"`
}

type DidChangeWorkspaceFoldersParams struct {
	Event WorkspaceFoldersChangeEvent `json:"event"`
}

type RenameOptions struct {
	PrepareProvider bool `json:"prepareProvider,omitempty"`
}

type ReferenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

type ReferenceParams struct {
	TextDocumentPositionParams
	Context ReferenceContext `json:"context"`
}

type RenameParams struct {
	TextDocumentPositionParams
	NewName string `json:"newName"`
}

type PrepareRenameResult struct {
	Range       Range  `json:"range"`
	Placeholder string `json:"placeholder"`
}

type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

type WorkspaceEdit struct {
	Changes map[DocumentURI][]TextEdit `json:"changes,omitempty"`
}

// HierarchyItem is a call or type hierarchy item: they have the same
// shape.
type HierarchyItem struct {
	Name           string          `json:"name"`
	Kind           SymbolKind      `json:"kind"`
	Tags           []SymbolTag     `json:"tags,omitempty"`
	Detail         string          `json:"detail,omitempty"`
	URI            DocumentURI     `json:"uri"`
	Range          Range           `json:"range"`
	SelectionRange Range           `json:"selectionRange"`
	Data           json.RawMessage `json:"data,omitempty"`
}

type HierarchyItemParams struct {
	Item HierarchyItem `json:"item"`
}

type CallHierarchyIncomingCall struct {
	From       HierarchyItem `json:"from"`
	FromRanges []Range       `json:"fromRanges"`
}

type CallHierarchyOutgoingCall struct {
	To         HierarchyItem `json:"to"`
	FromRanges []Range       `json:"fromRanges"`
}

type WorkspaceSymbolParams struct {
	Query string `json:"query"`
}

// ---- completion and signature help ----

type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
	ResolveProvider   bool     `json:"resolveProvider,omitempty"`
}

type SignatureHelpOptions struct {
	TriggerCharacters   []string `json:"triggerCharacters,omitempty"`
	RetriggerCharacters []string `json:"retriggerCharacters,omitempty"`
}

type CompletionContext struct {
	TriggerKind      int    `json:"triggerKind"`
	TriggerCharacter string `json:"triggerCharacter,omitempty"`
}

type CompletionParams struct {
	TextDocumentPositionParams
	Context *CompletionContext `json:"context,omitempty"`
}

type CompletionItemKind int

const (
	CompletionText          CompletionItemKind = 1
	CompletionMethod        CompletionItemKind = 2
	CompletionFunction      CompletionItemKind = 3
	CompletionConstructor   CompletionItemKind = 4
	CompletionField         CompletionItemKind = 5
	CompletionVariable      CompletionItemKind = 6
	CompletionClass         CompletionItemKind = 7
	CompletionInterface     CompletionItemKind = 8
	CompletionModule        CompletionItemKind = 9
	CompletionProperty      CompletionItemKind = 10
	CompletionEnum          CompletionItemKind = 13
	CompletionKeyword       CompletionItemKind = 14
	CompletionSnippet       CompletionItemKind = 15
	CompletionEnumMember    CompletionItemKind = 20
	CompletionConstant      CompletionItemKind = 21
	CompletionStruct        CompletionItemKind = 22
	CompletionTypeParameter CompletionItemKind = 25
)

const (
	PlainTextFormat = 1
	SnippetFormat   = 2
)

type CompletionItemLabelDetails struct {
	Detail      string `json:"detail,omitempty"`
	Description string `json:"description,omitempty"`
}

type CompletionItem struct {
	Label               string                      `json:"label"`
	LabelDetails        *CompletionItemLabelDetails `json:"labelDetails,omitempty"`
	Kind                CompletionItemKind          `json:"kind,omitempty"`
	Tags                []int                       `json:"tags,omitempty"`
	Detail              string                      `json:"detail,omitempty"`
	Documentation       *MarkupContent              `json:"documentation,omitempty"`
	Deprecated          bool                        `json:"deprecated,omitempty"`
	Preselect           bool                        `json:"preselect,omitempty"`
	SortText            string                      `json:"sortText,omitempty"`
	FilterText          string                      `json:"filterText,omitempty"`
	InsertTextFormat    int                         `json:"insertTextFormat,omitempty"`
	InsertTextMode      int                         `json:"insertTextMode,omitempty"`
	TextEdit            *TextEdit                   `json:"textEdit,omitempty"`
	AdditionalTextEdits []TextEdit                  `json:"additionalTextEdits,omitempty"`
	Command             *Command                    `json:"command,omitempty"`
	Data                json.RawMessage             `json:"data,omitempty"`
}

// InsertAsIs is the InsertTextMode of a completion whose text's
// indentation is as written.
const InsertAsIs = 1

type Command struct {
	Title     string `json:"title"`
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

type SignatureHelpContext struct {
	TriggerKind         int            `json:"triggerKind"`
	TriggerCharacter    string         `json:"triggerCharacter,omitempty"`
	IsRetrigger         bool           `json:"isRetrigger"`
	ActiveSignatureHelp *SignatureHelp `json:"activeSignatureHelp,omitempty"`
}

type SignatureHelpParams struct {
	TextDocumentPositionParams
	Context *SignatureHelpContext `json:"context,omitempty"`
}

type ParameterInformation struct {
	// Label is the parameter's offsets in its signature's label, [start,
	// end) in UTF-16 code units, or its text.
	Label         any            `json:"label"`
	Documentation *MarkupContent `json:"documentation,omitempty"`
}

type SignatureInformation struct {
	Label           string                 `json:"label"`
	Documentation   *MarkupContent         `json:"documentation,omitempty"`
	Parameters      []ParameterInformation `json:"parameters,omitempty"`
	ActiveParameter *uint32                `json:"activeParameter,omitempty"`
}

type SignatureHelp struct {
	Signatures      []SignatureInformation `json:"signatures"`
	ActiveSignature uint32                 `json:"activeSignature"`
	ActiveParameter uint32                 `json:"activeParameter"`
}

// Inlay hints.

type InlayHintKind int

const (
	InlayHintType      InlayHintKind = 1
	InlayHintParameter InlayHintKind = 2
)

type InlayHintParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
}

type InlayHint struct {
	Position     Position       `json:"position"`
	Label        string         `json:"label"`
	Kind         InlayHintKind  `json:"kind,omitempty"`
	Tooltip      *MarkupContent `json:"tooltip,omitempty"`
	PaddingLeft  bool           `json:"paddingLeft,omitempty"`
	PaddingRight bool           `json:"paddingRight,omitempty"`
}

// Code actions.

// The kinds of code actions kcls offers.
const (
	QuickFix              = "quickfix"
	RefactorRewrite       = "refactor.rewrite"
	SourceOrganizeImports = "source.organizeImports"
)

type CodeActionOptions struct {
	CodeActionKinds []string `json:"codeActionKinds,omitempty"`
	ResolveProvider bool     `json:"resolveProvider,omitempty"`
}

type CodeActionContext struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
	Only        []string     `json:"only,omitempty"`
	TriggerKind int          `json:"triggerKind,omitempty"`
}

type CodeActionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      CodeActionContext      `json:"context"`
}

type CodeAction struct {
	Title       string         `json:"title"`
	Kind        string         `json:"kind,omitempty"`
	Diagnostics []Diagnostic   `json:"diagnostics,omitempty"`
	IsPreferred bool           `json:"isPreferred,omitempty"`
	Edit        *WorkspaceEdit `json:"edit,omitempty"`
	Command     *Command       `json:"command,omitempty"`
}

type DidChangeConfigurationParams struct {
	Settings json.RawMessage `json:"settings"`
}
