package adminapi

import (
	"fmt"
	"maps"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/jsonschema-go/jsonschema"
)

// catalog.go registers administrative operation descriptions alongside their
// existing HTTP handlers. Descriptions are declared during package
// initialization and retain stable identifiers independently of handler names.
// Each running router selects its own catalogue from the routes actually
// mounted on that engine, so disabled or absent endpoints cannot become
// callable merely because another module declared a description.
//
// Registration checks the declared method and absolute path against the Gin
// group. Catalogue construction then rejects missing descriptions and duplicate
// identifiers before the MCP endpoint is published. Tests can construct isolated
// catalogues from explicit descriptions; production declarations are protected
// while they are copied and are never changed by request execution.

const (
	// Response modes select shared result handling; aliases retain their own
	// permission identity while resolving execution through another registered route.
	ResponseJSON    = "json"
	ResponseSSE     = "sse"
	ResponseFile    = "file"
	ResponseBrowser = "browser"
)

var (
	// declarations holds startup descriptions shared by registered HTTP modules.
	// The lock allows independent router fixtures to read declarations concurrently.
	declarationsMu sync.RWMutex
	declarations   = map[string]*Operation{}
)

// Operation describes one permission-bearing management action and its HTTP representation.
type Operation struct {
	ID          string `json:"operation_id"`       // stable permission identifier
	Module      string `json:"module"`             // management feature category
	Title       string `json:"title"`              // short operation label
	TitleZH     string `json:"title_zh,omitempty"` // optional Chinese operation label
	Description string `json:"description"`        // argument and behavior guidance
	Method      string `json:"method"`             // registered HTTP verb
	Path        string `json:"path"`               // absolute Gin route pattern
	ReadOnly    bool   `json:"read_only"`          // declared absence of mutations
	Sensitive   bool   `json:"sensitive"`          // browser confirmation requirement
	StepUp      bool   `json:"step_up"`            // existing recent TOTP requirement
	// StepUpWhen matches normalized path/query/body arguments that trigger an
	// existing conditional HTTP requirement; nil leaves only the unconditional StepUp flag.
	StepUpWhen     *jsonschema.Schema   `json:"step_up_when,omitempty"`
	HTTPUpgrade    string               `json:"http_upgrade,omitempty"`    // original HTTP upgrade protocol
	Response       string               `json:"response_mode"`             // shared response handling
	TerminalEvent  string               `json:"terminal_event,omitempty"`  // required successful SSE terminal
	BrowserPath    string               `json:"browser_path,omitempty"`    // same-site interactive destination
	Alias          string               `json:"alias,omitempty"`           // equivalent registered operation
	Idempotent     bool                 `json:"idempotent"`                // existing response idempotency support
	Input          *jsonschema.Schema   `json:"input_schema"`              // path query body definition
	Output         *jsonschema.Schema   `json:"response_schema,omitempty"` // original HTTP JSON response shape
	Examples       []map[string]any     `json:"examples,omitempty"`        // illustrative operation argument sets
	resolved       *jsonschema.Resolved // immutable compiled input validator
	stepUpResolved *jsonschema.Resolved // compiled conditional verification rule
}

// Catalog contains the operations mounted on one application router instance.
type Catalog struct {
	byID    map[string]*Operation // mounted operations by identity
	ordered []*Operation          // stable discovery presentation order
}

// Define retains one startup description after verifying its identity and
// compiling its schema. Invalid or duplicate declarations panic during startup,
// before any administrative request can reach the described operation.
//
// @project-doc docs/interfaces/mcp_management.md#operation_catalog
func Define(op Operation) *Operation {
	if op.ID == "" || !strings.HasPrefix(op.Path, "/api/v1/") || op.Input == nil {
		panic("invalid administrative operation declaration: " + op.ID)
	}
	if op.Response == "" {
		op.Response = ResponseJSON
	}
	if op.Output == nil && op.Response == ResponseJSON && op.HTTPUpgrade == "" {
		op.Output = JSONResponse(nil)
	}
	if op.Response == ResponseBrowser {
		// Browser handoffs collect credentials in the existing page. Keep the HTTP
		// body definition for documentation, but do not require it to request that handoff.
		input := *op.Input
		input.Required = slices.DeleteFunc(slices.Clone(input.Required), func(name string) bool { return name == "body" })
		op.Input = &input
	}
	resolved, err := op.Input.Resolve(nil)
	if err != nil {
		panic(fmt.Sprintf("operation %s schema: %v", op.ID, err))
	}
	op.resolved = resolved
	if op.StepUpWhen != nil {
		op.stepUpResolved, err = op.StepUpWhen.Resolve(nil)
		if err != nil {
			panic(fmt.Sprintf("operation %s step-up condition: %v", op.ID, err))
		}
	}
	declarationsMu.Lock()
	defer declarationsMu.Unlock()
	key := op.Method + " " + op.Path
	if _, exists := declarations[key]; exists {
		panic("duplicate administrative route description: " + key)
	}
	declarations[key] = &op
	return &op
}

// Register mounts a described operation using the supplied complete handler
// chain. The group's absolute prefix must contain the declared route; existing
// authentication and other group middleware remain attached by Gin.
func Register(group *gin.RouterGroup, op *Operation, handlers ...gin.HandlerFunc) {
	base := strings.TrimSuffix(group.BasePath(), "/")
	if op.Path != base && !strings.HasPrefix(op.Path, base+"/") {
		panic("operation outside registration group: " + op.ID)
	}
	relative := strings.TrimPrefix(op.Path, base)
	if path.Join(base, relative) != op.Path {
		panic("non-canonical administrative path: " + op.ID)
	}
	group.Handle(op.Method, relative, handlers...)
}

// FromRoutes selects descriptions for every mounted administrator route.
// Missing metadata prevents publication; aliases must refer to another mounted
// operation so their target cannot escape the registered management surface.
func FromRoutes(routes gin.RoutesInfo) (*Catalog, error) {
	declarationsMu.RLock()
	available := maps.Clone(declarations)
	declarationsMu.RUnlock()
	var operations []*Operation
	for _, route := range routes {
		op := available[route.Method+" "+route.Path]
		if !strings.HasPrefix(route.Path, "/api/v1/admin/") && op == nil {
			continue
		}
		if op == nil {
			return nil, fmt.Errorf("missing management operation: %s %s", route.Method, route.Path)
		}
		operations = append(operations, op)
	}
	return NewCatalog(operations)
}

// NewCatalog checks and indexes an explicit set of immutable descriptions.
// Duplicate identifiers and missing or chained alias targets return errors;
// callers publish the catalogue only after this validation succeeds.
func NewCatalog(operations []*Operation) (*Catalog, error) {
	c := &Catalog{byID: make(map[string]*Operation), ordered: slices.Clone(operations)}
	for _, op := range operations {
		if op.HTTPUpgrade != "" && (op.HTTPUpgrade != "websocket" || op.Method != http.MethodGet) {
			return nil, fmt.Errorf("unsupported HTTP upgrade: %s", op.ID)
		}
		if op.StepUpWhen != nil {
			if _, err := op.StepUpWhen.Resolve(nil); err != nil {
				return nil, fmt.Errorf("invalid step-up condition: %s: %w", op.ID, err)
			}
		}
		switch op.Response {
		case "", ResponseJSON, ResponseFile:
		case ResponseSSE:
			if op.TerminalEvent == "" {
				return nil, fmt.Errorf("missing stream terminal event: %s", op.ID)
			}
		case ResponseBrowser:
			if !strings.HasPrefix(op.BrowserPath, "/admin/") || strings.ContainsAny(op.BrowserPath, "\\\r\n") {
				return nil, fmt.Errorf("invalid browser continuation: %s", op.ID)
			}
		default:
			return nil, fmt.Errorf("unknown response handling: %s", op.ID)
		}
		if _, exists := c.byID[op.ID]; exists {
			return nil, fmt.Errorf("duplicate management operation: %s", op.ID)
		}
		c.byID[op.ID] = op
	}
	for _, op := range operations {
		if op.Alias == "" {
			continue
		}
		target := c.byID[op.Alias]
		if target == nil || target.Alias != "" || !target.ReadOnly || !op.ReadOnly || target.Sensitive || target.StepUp || target.StepUpWhen != nil {
			return nil, fmt.Errorf("invalid management alias: %s", op.ID)
		}
	}
	slices.SortFunc(c.ordered, func(a, b *Operation) int { return strings.Compare(a.ID, b.ID) })
	return c, nil
}

// Get returns the immutable mounted operation for an exact permission identifier.
func (c *Catalog) Get(id string) (*Operation, bool) {
	op, ok := c.byID[id]
	return op, ok
}

// Operations returns a new slice containing immutable mounted operation descriptions.
func (c *Catalog) Operations() []*Operation {
	return slices.Clone(c.ordered)
}

// Validate checks decoded tool arguments against the operation's resolved schema.
// Validator details are intentionally omitted because they can include submitted
// credentials; the public error identifies the operation and requests schema review.
func (o *Operation) Validate(arguments any) error {
	resolved := o.resolved
	if resolved == nil {
		var err error
		resolved, err = o.Input.Resolve(nil)
		if err != nil {
			return fmt.Errorf("invalid operation schema: %s", o.ID)
		}
	}
	if err := resolved.Validate(arguments); err != nil {
		return fmt.Errorf("arguments do not match the schema for %s; use describe_operation", o.ID)
	}
	return nil
}

// RequiresStepUp evaluates the route's unconditional or input-dependent verification rule.
// Nil arguments mean discovery has no concrete request yet, so a conditional
// rule is reported as potentially required. Execution passes validated arguments.
func (o *Operation) RequiresStepUp(arguments any) bool {
	if o.StepUp {
		return true
	}
	if o.StepUpWhen == nil {
		return false
	}
	if arguments == nil {
		return true
	}
	resolved := o.stepUpResolved
	if resolved == nil {
		// Isolated catalogue fixtures can supply unregistered descriptions. Resolve
		// their condition locally while keeping shared operation descriptions immutable during calls.
		var err error
		resolved, err = o.StepUpWhen.Resolve(nil)
		if err != nil {
			return true
		}
	}
	return resolved.Validate(arguments) == nil
}
