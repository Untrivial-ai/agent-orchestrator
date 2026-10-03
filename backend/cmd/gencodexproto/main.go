// Command gencodexproto generates Go bindings for the Codex app-server protocol
// from the schema the provider itself publishes.
//
// The protocol is a machine-readable contract: `codex app-server
// generate-json-schema --out DIR` writes the whole method and payload surface as
// draft-07 JSON Schema. Reading it beats hand-writing structs, because the
// failure mode of hand-writing is silent: a field spelled slightly wrong
// unmarshals to a zero value and the driver reports "no output" rather than
// "wrong shape". Every payload here is a compile-time type instead.
//
// Usage:
//
//	go run ./cmd/gencodexproto -out internal/adapters/chatdriver/codexappserver/codexproto/protocol.gen.go
//
// With no -schema, the generator asks the installed provider for its schema, so
// what lands in the tree always describes a real build rather than a snapshot
// someone remembered to update.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	var (
		schemaDir = flag.String("schema", "", "directory of JSON Schema files; empty asks the installed provider")
		out       = flag.String("out", "", "path of the Go file to write (required)")
		binary    = flag.String("codex", "codex", "provider binary used when -schema is empty")
	)
	flag.Parse()

	if *out == "" {
		fatal("-out is required")
	}

	dir := *schemaDir
	version := "unknown"
	if dir == "" {
		tmp, err := os.MkdirTemp("", "codexschema")
		if err != nil {
			fatal("temp dir: %v", err)
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		if err := emitSchema(*binary, tmp); err != nil {
			fatal("%v", err)
		}
		dir = tmp
	}
	if v, err := providerVersion(*binary); err == nil {
		version = v
	}

	g, err := load(dir)
	if err != nil {
		fatal("%v", err)
	}
	g.version = version

	if err := os.MkdirAll(filepath.Dir(*out), 0o750); err != nil {
		fatal("mkdir: %v", err)
	}

	src := g.render()
	formatted, err := format.Source(src)
	if err != nil {
		// Write the unformatted source next to the target so the syntax error is
		// readable; a gofmt failure here is a generator bug, not a schema problem.
		_ = os.WriteFile(*out+".broken", src, 0o600)
		fatal("gofmt: %v (unformatted source at %s.broken)", err, *out)
	}
	if err := os.WriteFile(*out, formatted, 0o600); err != nil {
		fatal("write: %v", err)
	}
	fmt.Fprintf(os.Stderr, "gencodexproto: %d types, %d methods from %s\n",
		len(g.defs), len(g.methods), version)
}

func fatal(formatText string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "gencodexproto: "+formatText+"\n", args...)
	os.Exit(1)
}

func emitSchema(binary, dir string) error {
	cmd := exec.Command(binary, "app-server", "generate-json-schema", "--out", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s app-server generate-json-schema: %w: %s", binary, err, out)
	}
	return nil
}

func providerVersion(binary string) (string, error) {
	out, err := exec.Command(binary, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

/* ---- schema model ------------------------------------------------------- */

// schema is the subset of draft-07 the Codex protocol actually uses. Verified by
// walking every published file: no patternProperties, no const, no if/then.
type schema struct {
	Ref                  string             `json:"$ref"`
	Type                 json.RawMessage    `json:"type"`
	Enum                 []any              `json:"enum"`
	Properties           map[string]*schema `json:"properties"`
	Required             []string           `json:"required"`
	Items                *schema            `json:"items"`
	AdditionalProperties json.RawMessage    `json:"additionalProperties"`
	OneOf                []*schema          `json:"oneOf"`
	AnyOf                []*schema          `json:"anyOf"`
	AllOf                []*schema          `json:"allOf"`
	Title                string             `json:"title"`
	Description          string             `json:"description"`
	Format               string             `json:"format"`
	Definitions          map[string]*schema `json:"definitions"`

	// permissive marks a schema written as the bare literal `true` or `false`.
	permissive bool
}

// UnmarshalJSON accepts draft-07's boolean schemas. `true` means "any value" and
// `false` means "no value"; both render as raw JSON, which is the only honest Go
// type for a payload the schema declines to describe.
func (s *schema) UnmarshalJSON(data []byte) error {
	var anything bool
	if err := json.Unmarshal(data, &anything); err == nil {
		*s = schema{permissive: true}
		return nil
	}
	type plain schema // no UnmarshalJSON, so this does not recurse
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*s = schema(p)
	return nil
}

// types reports the declared JSON types, which the schema writes either as a
// string or as a list including "null" for an optional value.
func (s *schema) types() []string {
	if len(s.Type) == 0 {
		return nil
	}
	var one string
	if err := json.Unmarshal(s.Type, &one); err == nil {
		return []string{one}
	}
	var many []string
	if err := json.Unmarshal(s.Type, &many); err == nil {
		return many
	}
	return nil
}

// nullable reports whether the value may be absent, which decides whether the
// Go field is a pointer.
func (s *schema) nullable() bool {
	for _, t := range s.types() {
		if t == "null" {
			return true
		}
	}
	// anyOf: [{...}, {"type": "null"}] is how the schema spells an optional $ref.
	for _, v := range s.AnyOf {
		for _, t := range v.types() {
			if t == "null" {
				return true
			}
		}
	}
	return false
}

// base strips the null arm so the remaining schema describes the value itself.
func (s *schema) base() *schema {
	if len(s.AnyOf) > 0 {
		var concrete []*schema
		for _, v := range s.AnyOf {
			if len(v.types()) == 1 && v.types()[0] == "null" {
				continue
			}
			concrete = append(concrete, v)
		}
		if len(concrete) == 1 {
			return concrete[0]
		}
	}
	// allOf with a single arm is a wrapper the schema uses to attach a description
	// to a $ref.
	if len(s.AllOf) == 1 && s.Ref == "" && len(s.Properties) == 0 {
		return s.AllOf[0]
	}
	return s
}

/* ---- method table ------------------------------------------------------- */

type method struct {
	Name      string // wire method, e.g. "thread/start"
	Direction string // ClientRequest | ClientNotification | ServerRequest | ServerNotification
	Params    string // Go type of params, empty when the method carries none
	Result    string // Go type of the result, empty for notifications
	Doc       string
}

type generator struct {
	version string
	defs    map[string]*schema // merged definitions, by name
	methods []method
}

// unionFiles map a published union file to the direction it describes.
var unionFiles = map[string]string{
	"ClientRequest.json":      "ClientRequest",
	"ClientNotification.json": "ClientNotification",
	"ServerRequest.json":      "ServerRequest",
	"ServerNotification.json": "ServerNotification",
}

func load(dir string) (*generator, error) {
	g := &generator{defs: map[string]*schema{}}

	// The v2 bundle carries every type including the response shapes, which the
	// per-direction union files omit.
	bundle := filepath.Join(dir, "codex_app_server_protocol.v2.schemas.json")
	if err := g.mergeDefs(bundle); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(unionFiles))
	for f := range unionFiles {
		names = append(names, f)
	}
	sort.Strings(names)

	for _, file := range names {
		path := filepath.Join(dir, file)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := g.mergeDefs(path); err != nil {
			return nil, err
		}
		if err := g.collectMethods(path, unionFiles[file]); err != nil {
			return nil, err
		}
	}
	if len(g.methods) == 0 {
		return nil, fmt.Errorf("no methods found in %s; is this a codex schema directory?", dir)
	}
	sort.Slice(g.methods, func(i, j int) bool {
		if g.methods[i].Direction != g.methods[j].Direction {
			return g.methods[i].Direction < g.methods[j].Direction
		}
		return g.methods[i].Name < g.methods[j].Name
	})
	return g, nil
}

// mergeDefs folds one file's definitions into the shared namespace. The union
// files repeat types the bundle already declares; a genuine disagreement is a
// generator-stopping error rather than a silently chosen winner.
func (g *generator) mergeDefs(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	var doc schema
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	for name, def := range doc.Definitions {
		existing, ok := g.defs[name]
		if !ok {
			g.defs[name] = def
			continue
		}
		if !sameShape(existing, def) {
			return fmt.Errorf("definition %q differs between files (%s); the generator cannot pick a winner",
				name, filepath.Base(path))
		}
	}
	return nil
}

// sameShape compares what the generator reads and ignores what it only echoes.
// The bundle carries `title` and `$schema` that the per-direction files omit, so
// comparing documentation would report a conflict on every shared definition.
func sameShape(a, b *schema) bool {
	ja, err1 := json.Marshal(stripDocs(a))
	jb, err2 := json.Marshal(stripDocs(b))
	return err1 == nil && err2 == nil && bytes.Equal(ja, jb)
}

func stripDocs(s *schema) *schema {
	if s == nil {
		return nil
	}
	out := *s
	out.Title = ""
	out.Description = ""
	if s.Properties != nil {
		out.Properties = make(map[string]*schema, len(s.Properties))
		for k, v := range s.Properties {
			out.Properties[k] = stripDocs(v)
		}
	}
	if s.Definitions != nil {
		out.Definitions = make(map[string]*schema, len(s.Definitions))
		for k, v := range s.Definitions {
			out.Definitions[k] = stripDocs(v)
		}
	}
	out.Items = stripDocs(s.Items)
	for _, list := range []struct {
		src *[]*schema
		dst *[]*schema
	}{{&s.OneOf, &out.OneOf}, {&s.AnyOf, &out.AnyOf}, {&s.AllOf, &out.AllOf}} {
		if *list.src == nil {
			continue
		}
		copied := make([]*schema, len(*list.src))
		for i, v := range *list.src {
			copied[i] = stripDocs(v)
		}
		*list.dst = copied
	}
	return &out
}

// collectMethods reads a direction's `oneOf`, where each arm is an envelope whose
// `method` is a single-value enum and whose `params` points at the payload.
func (g *generator) collectMethods(path, direction string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc schema
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	for _, arm := range doc.OneOf {
		methodProp, ok := arm.Properties["method"]
		if !ok || len(methodProp.Enum) != 1 {
			continue
		}
		name, ok := methodProp.Enum[0].(string)
		if !ok {
			continue
		}
		m := method{Name: name, Direction: direction, Doc: arm.Description}
		if params, ok := arm.Properties["params"]; ok {
			m.Params = refName(params.base().Ref)
		}
		// Requests answer with `<Params-stem>Response` when the bundle declares one.
		if strings.HasSuffix(direction, "Request") && m.Params != "" {
			if stem, ok := strings.CutSuffix(m.Params, "Params"); ok {
				if _, exists := g.defs[stem+"Response"]; exists {
					m.Result = stem + "Response"
				}
			}
		}
		g.methods = append(g.methods, m)
	}
	return nil
}

func refName(ref string) string {
	if ref == "" {
		return ""
	}
	return exportName(ref[strings.LastIndex(ref, "/")+1:])
}

/* ---- Go type mapping ---------------------------------------------------- */

// goType renders the Go type for a schema. `owner` and `field` name inline
// object types, which the schema declares anonymously inside properties.
func (g *generator) goType(s *schema, owner, field string, inline *[]inlineType) string {
	s = s.base()

	if s.Ref != "" {
		return refName(s.Ref)
	}
	if len(s.AllOf) == 1 {
		return g.goType(s.AllOf[0], owner, field, inline)
	}

	// A string enum inline in a property: the schema names most of these through a
	// $ref, so what is left is a one-off. Rendering it as a plain string keeps the
	// generated surface small without losing any wire information.
	if len(s.Enum) > 0 {
		return "string"
	}

	ts := s.types()
	concrete := ""
	for _, t := range ts {
		if t != "null" {
			concrete = t
			break
		}
	}

	switch concrete {
	case "string":
		return "string"
	case "boolean":
		return "bool"
	case "integer":
		if s.Format == "uint64" || s.Format == "uint32" {
			return "uint64"
		}
		return "int64"
	case "number":
		return "float64"
	case "array":
		if s.Items == nil {
			return "[]json.RawMessage"
		}
		return "[]" + g.goType(s.Items, owner, field, inline)
	case "object":
		// A map when the schema constrains additional properties and declares no
		// fixed fields; a named struct otherwise.
		if len(s.Properties) == 0 && len(s.AdditionalProperties) > 0 {
			var sub schema
			if err := json.Unmarshal(s.AdditionalProperties, &sub); err == nil && len(s.AdditionalProperties) > 0 {
				if string(s.AdditionalProperties) != "true" && string(s.AdditionalProperties) != "false" {
					return "map[string]" + g.goType(&sub, owner, field, inline)
				}
			}
			return "map[string]json.RawMessage"
		}
		if len(s.Properties) == 0 {
			return "json.RawMessage"
		}
		name := exportName(owner + "_" + field)
		*inline = append(*inline, inlineType{name: name, s: s})
		return name
	}

	// oneOf/anyOf that survived: a union the generator will not guess at. Left as
	// raw JSON so the caller decides, rather than picking an arm and being wrong.
	if len(s.OneOf) > 0 || len(s.AnyOf) > 0 {
		if enumUnion(s) {
			return "string"
		}
		return "json.RawMessage"
	}
	return "json.RawMessage"
}

type inlineType struct {
	name string
	s    *schema
}

// enumUnion reports a oneOf whose every arm is a documented string enum — the
// schema's way of writing a Rust enum of unit variants.
func enumUnion(s *schema) bool {
	arms := s.OneOf
	if len(arms) == 0 {
		arms = s.AnyOf
	}
	if len(arms) == 0 {
		return false
	}
	for _, a := range arms {
		if len(a.Enum) == 0 {
			return false
		}
		for _, t := range a.types() {
			if t != "string" && t != "null" {
				return false
			}
		}
	}
	return true
}

// taggedUnion reports the discriminator shared by every arm of a oneOf, if there
// is one. Codex uses this for its Rust enums with struct variants.
func taggedUnion(s *schema) (string, bool) {
	arms := s.OneOf
	if len(arms) < 2 {
		return "", false
	}
	var common map[string]bool
	for _, a := range arms {
		if a.Properties == nil {
			return "", false
		}
		here := map[string]bool{}
		for name, p := range a.Properties {
			if len(p.Enum) == 1 {
				here[name] = true
			}
		}
		if len(here) == 0 {
			return "", false
		}
		if common == nil {
			common = here
			continue
		}
		for name := range common {
			if !here[name] {
				delete(common, name)
			}
		}
	}
	keys := make([]string, 0, len(common))
	for name := range common {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "", false
	}
	return keys[0], true
}

/* ---- rendering ---------------------------------------------------------- */

func (g *generator) render() []byte {
	var b strings.Builder

	digest := g.digest()
	fmt.Fprintf(&b, "// Code generated by cmd/gencodexproto. DO NOT EDIT.\n//\n")
	fmt.Fprintf(&b, "// Source: %s app-server generate-json-schema\n", g.version)
	fmt.Fprintf(&b, "// Method surface: %s\n", digest)
	b.WriteString(`//
// Regenerate with:
//
//	go generate ./internal/adapters/chatdriver/codexappserver/...
//
// The provider marks app-server [experimental], so this file describes one build
// rather than a stable contract. ProtocolDigest below is what the driver's
// conformance test compares against a live provider, so a protocol change shows
// up as a failing test instead of a field that quietly stopped arriving.
//
// The package doc lives in gen.go.

package codexproto

import "encoding/json"

`)

	fmt.Fprintf(&b, "// ProviderVersion is the build whose schema produced this file.\nconst ProviderVersion = %q\n\n", g.version)
	fmt.Fprintf(&b, "// ProtocolDigest fingerprints the generated method surface.\nconst ProtocolDigest = %q\n\n", digest)

	g.renderMethods(&b)
	g.renderTypes(&b)
	return []byte(b.String())
}

func (g *generator) digest() string {
	h := sha256.New()
	names := make([]string, 0, len(g.methods))
	for _, m := range g.methods {
		names = append(names, m.Direction+" "+m.Name)
	}
	sort.Strings(names)
	for _, name := range names {
		_, _ = fmt.Fprintln(h, name)
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func (g *generator) renderMethods(b *strings.Builder) {
	b.WriteString("// Method names, one constant per method the provider declares. A typo in a\n")
	b.WriteString("// method name is a compile error rather than a silent -32601 at runtime.\nconst (\n")
	seen := map[string]bool{}
	for _, m := range g.methods {
		name := "Method" + exportName(strings.NewReplacer("/", "_", ".", "_").Replace(m.Name))
		if seen[name] {
			continue
		}
		seen[name] = true
		if m.Doc != "" && m.Doc != "NEW APIs" && m.Doc != "NEW NOTIFICATIONS" {
			fmt.Fprintf(b, "\t// %s\n", oneLine(m.Doc))
		}
		fmt.Fprintf(b, "\t%s = %q\n", name, m.Name)
	}
	b.WriteString(")\n\n")

	// A table rather than a switch: the driver's conformance test walks it to
	// assert that every method AO claims to handle still exists upstream.
	b.WriteString("// MethodInfo describes one method's direction and payload types.\ntype MethodInfo struct {\n")
	b.WriteString("\tName      string\n\tDirection string\n\t// Params and Result name the generated types, empty when the method has none.\n")
	b.WriteString("\tParams string\n\tResult string\n}\n\n")

	b.WriteString("// Methods is every method the generating provider declared.\nvar Methods = []MethodInfo{\n")
	for _, m := range g.methods {
		fmt.Fprintf(b, "\t{Name: %q, Direction: %q, Params: %q, Result: %q},\n",
			m.Name, m.Direction, m.Params, m.Result)
	}
	b.WriteString("}\n\n")

	b.WriteString(`// Declares reports whether the generating provider declared this method.
func Declares(method string) bool {
	for _, m := range Methods {
		if m.Name == method {
			return true
		}
	}
	return false
}

`)
}

func (g *generator) renderTypes(b *strings.Builder) {
	names := make([]string, 0, len(g.defs))
	for name := range g.defs {
		names = append(names, name)
	}
	sort.Strings(names)

	// Inline object types discovered while rendering fields are appended as they
	// are found, so the loop walks a growing list.
	var inline []inlineType
	emitted := map[string]bool{}

	for _, name := range names {
		g.renderDef(b, exportName(name), g.defs[name], &inline, emitted)
	}
	for i := 0; i < len(inline); i++ {
		it := inline[i]
		if emitted[it.name] {
			continue
		}
		g.renderStruct(b, it.name, it.s, &inline, emitted)
	}
}

func (g *generator) renderDef(b *strings.Builder, name string, s *schema, inline *[]inlineType, emitted map[string]bool) {
	constantNames := map[string]int{}
	constantName := func(value string) string {
		base := name + exportName(value)
		constantNames[base]++
		if constantNames[base] > 1 {
			return fmt.Sprintf("%s%d", base, constantNames[base])
		}
		return base
	}

	if emitted[name] {
		return
	}

	// A named string enum: a Go string type plus its values, so an invalid value
	// cannot be constructed by accident.
	if len(s.Enum) > 0 && isStringEnum(s) {
		emitted[name] = true
		doc(b, name, s.Description)
		fmt.Fprintf(b, "type %s string\n\nconst (\n", name)
		for _, v := range s.Enum {
			str, ok := v.(string)
			if !ok {
				continue
			}
			fmt.Fprintf(b, "\t%s %s = %q\n", constantName(str), name, str)
		}
		b.WriteString(")\n\n")
		return
	}

	if enumUnion(s) {
		emitted[name] = true
		doc(b, name, s.Description)
		fmt.Fprintf(b, "type %s string\n\nconst (\n", name)
		arms := s.OneOf
		if len(arms) == 0 {
			arms = s.AnyOf
		}
		seen := map[string]bool{}
		for _, a := range arms {
			for _, v := range a.Enum {
				str, ok := v.(string)
				if !ok || seen[str] {
					continue
				}
				seen[str] = true
				if a.Description != "" {
					fmt.Fprintf(b, "\t// %s\n", oneLine(a.Description))
				}
				fmt.Fprintf(b, "\t%s %s = %q\n", constantName(str), name, str)
			}
		}
		b.WriteString(")\n\n")
		return
	}

	// A tagged union flattens to one struct with the discriminator typed and every
	// arm's fields optional. This is how the driver already reads thread items:
	// switch on the tag, then read the fields that tag implies. A Go sum type
	// would be more precise and much less usable at a call site.
	if tag, ok := taggedUnion(s); ok {
		emitted[name] = true
		g.renderFlattenedUnion(b, name, s, tag, inline, emitted)
		return
	}

	if len(s.Properties) > 0 {
		g.renderStruct(b, name, s, inline, emitted)
		return
	}

	// Everything else is an alias: a scalar, an array, a map, or a union the
	// generator declines to guess at.
	emitted[name] = true
	doc(b, name, s.Description)
	target := g.goType(s, name, "", inline)

	// A DEFINED type over json.RawMessage does not inherit its UnmarshalJSON, so
	// encoding/json falls back to treating it as []byte and demands a base64
	// string. Every numeric value then fails to decode — silently, if the caller
	// ignores the error. That cost us every `serverRequest/resolved` notification,
	// because request ids arrive as numbers. A type ALIAS keeps the methods, so
	// callers cannot be trapped by a name that looks decodable and is not.
	if target == "json.RawMessage" {
		fmt.Fprintf(b, "type %s = %s\n\n", name, target)
		return
	}
	fmt.Fprintf(b, "type %s %s\n\n", name, target)
}

func (g *generator) renderStruct(b *strings.Builder, name string, s *schema, inline *[]inlineType, emitted map[string]bool) {
	if emitted[name] {
		return
	}
	emitted[name] = true

	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}

	fields := make([]string, 0, len(s.Properties))
	for f := range s.Properties {
		fields = append(fields, f)
	}
	sort.Strings(fields)

	doc(b, name, s.Description)
	fmt.Fprintf(b, "type %s struct {\n", name)
	used := map[string]bool{}
	for _, f := range fields {
		p := s.Properties[f]
		g.renderField(b, name, f, p, required[f], used, inline)
	}
	b.WriteString("}\n\n")
}

func (g *generator) renderField(
	b *strings.Builder, owner, jsonName string, p *schema,
	required bool, used map[string]bool, inline *[]inlineType,
) {
	goName := exportName(jsonName)
	if goName == "" {
		return
	}
	// Two JSON names can normalize to one Go name (`type` and `Type`); keep the
	// first and leave the second reachable only as raw JSON rather than emitting
	// a struct that does not compile.
	if used[goName] {
		goName += "_"
	}
	used[goName] = true

	typ := g.goType(p, owner, jsonName, inline)
	tag := jsonName
	optional := !required || p.nullable()
	if optional {
		tag += ",omitempty"
	}
	// Pointers only where zero and absent differ and the caller would care: a
	// missing exit code is not exit code 0, but an absent slice and an empty one
	// are the same thing to every reader.
	if optional && needsPointer(typ) {
		typ = "*" + typ
	}
	if p.Description != "" {
		fmt.Fprintf(b, "\t// %s\n", oneLine(p.Description))
	}
	fmt.Fprintf(b, "\t%s %s `json:%q`\n", goName, typ, tag)
}

// widen finds a single Go type for two arms of a tagged union that name the same
// field differently. Both must reduce to the same JSON kind, or the result is raw
// JSON — a wrong type here would decode to a zero value silently, which is the
// exact failure this generator exists to prevent.
func (g *generator) widen(a, b string) string {
	ka, kb := g.jsonKind(a), g.jsonKind(b)
	if ka != kb || ka == "" {
		return "json.RawMessage"
	}
	switch ka {
	case "string":
		return "string"
	case "integer":
		return "int64"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	default:
		return "json.RawMessage"
	}
}

// jsonKind reduces a generated Go type to the JSON kind it encodes, resolving
// named types through the schema that produced them.
func (g *generator) jsonKind(typ string) string {
	typ = strings.TrimPrefix(typ, "*")
	switch typ {
	case "string":
		return "string"
	case "int64", "uint64":
		return "integer"
	case "float64":
		return "number"
	case "bool":
		return "boolean"
	case "json.RawMessage":
		return ""
	}
	if strings.HasPrefix(typ, "[]") || strings.HasPrefix(typ, "map[") {
		return ""
	}
	// A named definition: resolve one level. Every named type the protocol uses in
	// a conflicting position is a string enum or a scalar alias.
	for name, def := range g.defs {
		if exportName(name) != typ {
			continue
		}
		if isStringEnum(def) || enumUnion(def) {
			return "string"
		}
		base := def.base()
		for _, t := range base.types() {
			switch t {
			case "string", "integer", "number", "boolean":
				return t
			}
		}
		if len(base.AllOf) == 1 {
			return g.jsonKind(refName(base.AllOf[0].Ref))
		}
		return ""
	}
	return ""
}

func needsPointer(typ string) bool {
	switch {
	case strings.HasPrefix(typ, "[]"), strings.HasPrefix(typ, "map["):
		return false
	case typ == "json.RawMessage":
		return false
	default:
		return true
	}
}

// renderFlattenedUnion emits one struct covering every arm, with the shared
// discriminator first. A field that appears in more than one arm with the same
// type is emitted once.
func (g *generator) renderFlattenedUnion(
	b *strings.Builder, name string, s *schema, tag string,
	inline *[]inlineType, emitted map[string]bool,
) {
	type field struct {
		json     string
		typ      string
		desc     string
		required bool
	}
	order := []string{}
	byName := map[string]field{}
	tagValues := []string{}

	for _, arm := range s.OneOf {
		armRequired := map[string]bool{}
		for _, r := range arm.Required {
			armRequired[r] = true
		}
		props := make([]string, 0, len(arm.Properties))
		for p := range arm.Properties {
			props = append(props, p)
		}
		sort.Strings(props)

		for _, p := range props {
			ps := arm.Properties[p]
			if p == tag {
				for _, v := range ps.Enum {
					if str, ok := v.(string); ok {
						tagValues = append(tagValues, str)
					}
				}
				continue
			}
			typ := g.goType(ps, name, p, inline)
			existing, seen := byName[p]
			if !seen {
				order = append(order, p)
				byName[p] = field{json: p, typ: typ, desc: ps.Description, required: armRequired[p]}
				continue
			}
			// The same field name with a different type across arms needs one Go
			// type. Where the arms agree on the underlying JSON kind — six string
			// enums for `status`, four integer widths for `durationMs` — widening
			// to that kind keeps the field readable and stays true to the wire.
			// Where they genuinely disagree, raw JSON is the honest rendering.
			if existing.typ != typ {
				existing.typ = g.widen(existing.typ, typ)
				byName[p] = existing
			}
		}
	}

	tagType := name + "Type"
	doc(b, name, s.Description)
	fmt.Fprintf(b, "//\n// A tagged union: %s selects which fields carry values.\n", exportName(tag))
	fmt.Fprintf(b, "type %s struct {\n", name)
	fmt.Fprintf(b, "\t%s %s `json:%q`\n", exportName(tag), tagType, tag)

	used := map[string]bool{exportName(tag): true}
	for _, p := range order {
		f := byName[p]
		goName := exportName(p)
		if used[goName] {
			goName += "_"
		}
		used[goName] = true
		typ := f.typ
		// Every non-discriminator field is optional: it belongs to some arms only.
		if needsPointer(typ) {
			typ = "*" + typ
		}
		if f.desc != "" {
			fmt.Fprintf(b, "\t// %s\n", oneLine(f.desc))
		}
		fmt.Fprintf(b, "\t%s %s `json:\"%s,omitempty\"`\n", goName, typ, p)
	}
	b.WriteString("}\n\n")

	emitted[tagType] = true
	fmt.Fprintf(b, "// %s is the discriminator of %s.\ntype %s string\n\nconst (\n", tagType, name, tagType)
	seen := map[string]bool{}
	usedNames := map[string]bool{}
	sort.Strings(tagValues)
	for _, v := range tagValues {
		if seen[v] {
			continue
		}
		seen[v] = true
		constant := tagType + exportName(v)
		for usedNames[constant] {
			constant += "_"
		}
		usedNames[constant] = true
		fmt.Fprintf(b, "\t%s %s = %q\n", constant, tagType, v)
	}
	b.WriteString(")\n\n")
}

func isStringEnum(s *schema) bool {
	for _, t := range s.types() {
		if t == "string" {
			return true
		}
	}
	if len(s.types()) > 0 {
		return false
	}
	for _, v := range s.Enum {
		if _, ok := v.(string); !ok {
			return false
		}
	}
	return len(s.Enum) > 0
}

func doc(b *strings.Builder, name, description string) {
	if description == "" {
		return
	}
	fmt.Fprintf(b, "// %s: %s\n", name, oneLine(description))
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	s = strings.TrimSpace(s)
	const limit = 110
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	return s
}

// exportName turns a JSON or schema name into an exported Go identifier.
func exportName(s string) string {
	var b strings.Builder
	upper := true
	for _, r := range s {
		alpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		digit := r >= '0' && r <= '9'
		switch {
		// Anything that is not alphanumeric is a word boundary. The schema uses
		// `_`, `-`, `/`, `.` and, for JSON Schema's own keywords, `$`.
		case !alpha && !digit:
			upper = true
		case digit:
			b.WriteRune(r)
			upper = false
		case upper:
			b.WriteString(strings.ToUpper(string(r)))
			upper = false
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	// A Go identifier cannot begin with a digit.
	if out != "" && out[0] >= '0' && out[0] <= '9' {
		out = "N" + out
	}
	// Initialisms, so the generated names read like Go rather than like a
	// transliteration.
	for _, pair := range [][2]string{
		{"Id", "ID"}, {"Url", "URL"}, {"Uri", "URI"}, {"Api", "API"},
		{"Json", "JSON"}, {"Http", "HTTP"}, {"Mcp", "MCP"}, {"Sdp", "SDP"},
		{"Cwd", "Cwd"}, {"Ts", "Ts"},
	} {
		if strings.HasSuffix(out, pair[0]) {
			out = strings.TrimSuffix(out, pair[0]) + pair[1]
		}
	}
	return out
}
