# Evidence: config parsing / template rendering libraries

Facts gathered to inform a design decision about how mdc-lite should parse its config file
and render NFO / path templates. **No recommendations are made here.**

- Date of research: 2026-10-27
- Repo / cwd: `/root/clone/mdc-lite` (branch `research/config-libs`, forked from `main` @ `57cc25c`)
- Go toolchain: `go version go1.27.2 linux/amd64`
- Method: real temp modules under `/tmp`, real `go get` / `go run` / `go test`, module sources read
  from the module cache. Every command and its raw output is reproduced in
  [Appendix B](#appendix-b--raw-command-output). Module sources are in
  [Appendix A](#appendix-a--exact-test-programs).
- Network was reachable; all fetches succeeded on the first attempt.

## 0. Versions tested

| Library | Version tested | How obtained | Latest at time of research |
| --- | --- | --- | --- |
| `gopkg.in/yaml.v3` | `v3.0.1` | `go get gopkg.in/yaml.v3` | `v3.0.1` (`go list -m -versions` → `v3.0.0 v3.0.1`) |
| `github.com/BurntSushi/toml` | `v1.6.0` | `go get ...@latest` | `v1.6.0` |
| `github.com/pelletier/go-toml/v2` | `v2.4.3` | `go get ...@latest` | `v2.4.3` |
| `github.com/nikolalohinski/gonja/v2` | `v2.9.1` | `go get ...@latest` | `v2.9.1` |
| `github.com/fsnotify/fsnotify` | not installed | n/a | `v1.10.1` (`go list -m github.com/fsnotify/fsnotify@latest`) |

Exact commands:

```
$ cd /tmp/yamltest && go mod init x && go get gopkg.in/yaml.v3
$ cd /tmp/tomltest && go mod init t && go get github.com/BurntSushi/toml@latest github.com/pelletier/go-toml/v2@latest
$ cd /tmp/gonjatest && go mod init g && go get github.com/nikolalohinski/gonja/v2@latest
```

`go.mod` produced in `/tmp/tomltest`:

```
module t

go 1.27.2

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/pelletier/go-toml/v2 v2.4.3
)
```

---

## 1. Strict decoding with line numbers (YAML, `gopkg.in/yaml.v3` v3.0.1)

### 1.1 The API exists and is opt-in

`gopkg.in/yaml.v3@v3.0.1/yaml.go:108-112`:

```go
// KnownFields ensures that the keys in decoded mappings to
// exist as fields in the struct being decoded into.
func (dec *Decoder) KnownFields(enable bool) {
	dec.knownFields = enable
}
```

It is a method on `*Decoder` only. The package-level `yaml.Unmarshal` is **always
non-strict** — `gopkg.in/yaml.v3@v3.0.1/yaml.go:88-90`:

```go
func Unmarshal(in []byte, out interface{}) (err error) {
	return unmarshal(in, out, false)
}
```

### 1.2 It rejects unknown **struct** keys, and the error carries the line (not the column)

Test doc (nested struct, typo'd key inside a list element), from `/tmp/yamltest/main.go`:

```yaml
name: demo
steps:
  - type: scrape
    with:
      plot_prioirty: []
```

`Decode` with `KnownFields(true)` produces:

```
err type = *yaml.TypeError
err string = yaml: unmarshal errors:
  line 5: field plot_prioirty not found in type main.StepWith
contains "line": true
is *yaml.TypeError = true, len(Errors) = 1
  Errors[0] = "line 5: field plot_prioirty not found in type main.StepWith"
```

Source line 5 of the document is exactly `      plot_prioirty: []`, i.e. **the line of the
offending key**. There is **no column** in the message.

Other shapes, same program:

| Case | Error string (verbatim) |
| --- | --- |
| typo of a list-element field (`- tpye: scrape`, line 3) | `yaml: unmarshal errors:\n  line 3: field tpye not found in type main.Step` |
| typo at root (`nome: demo`, line 1) | `yaml: unmarshal errors:\n  line 1: field nome not found in type main.Pipeline` |
| two unknown keys in two different list elements (lines 4 and 7) | `yaml: unmarshal errors:\n  line 4: field nope not found in type main.Sub\n  line 7: field nada not found in type main.Sub` |

So: errors accumulate (one `Errors` entry per offending key), the error type is `*yaml.TypeError`
(`yaml.go:315-321`):

```go
type TypeError struct {
	Errors []string
}

func (e *TypeError) Error() string {
	return fmt.Sprintf("yaml: unmarshal errors:\n  %s", strings.Join(e.Errors, "\n  "))
}
```

The line number is baked into the string at the point of failure —
`gopkg.in/yaml.v3@v3.0.1/decode.go:943`:

```go
} else if d.knownFields {
	d.terrors = append(d.terrors, fmt.Sprintf("line %d: field %s not found in type %s", ni.Line, name.String(), out.Type()))
}
```

`ni.Line` is the line of the **key** node. No column is recorded anywhere in `TypeError`.

### 1.3 Known limitation found by test: `KnownFields` does not check `map[string]…` keys

Same document, but the `with:` field is declared as a map rather than a struct:

```go
type StepMap struct {
	Type string         `yaml:"type"`
	With map[string]any `yaml:"with,omitempty"`
}
```

```
=== D: unknown key inside map[string]any (steps[].with.plot_prioirty) ===
err = <nil>
```

i.e. `KnownFields(true)` **silently accepts** any key inside a map-valued field; it only checks
keys that must resolve to struct fields. This matches the doc comment ("keys in decoded mappings
to exist as fields in the struct being decoded into"). A map whose *values* are structs still gets
checked at the value level:

```
map[string]Sub, free map key + typo'd inner field err = "yaml: unmarshal errors:\n  line 5: field plot_prioirty not found in type main.Sub"
```

### 1.4 What `yaml.Node` adds when `TypeError` is not enough: line **and** column

`gopkg.in/yaml.v3@v3.0.1/yaml.go:412-415`:

```go
	// Line and Column hold the node position in the decoded YAML text.
	// These fields are not respected when encoding the node.
	Line   int
	Column int
```

Decoding the same document into `yaml.Node` and walking the tree gives (raw output):

```
Document line=1 col=1
  Mapping line=1 col=1
    "name" (Scalar) line=1 col=1
    "demo" (Scalar) line=1 col=7
    "steps" (Scalar) line=2 col=1
    Sequence line=3 col=3
      Mapping line=3 col=5
        "type" (Scalar) line=3 col=5
        "scrape" (Scalar) line=3 col=11
        "with" (Scalar) line=4 col=5
        Mapping line=5 col=7
          "plot_prioirty" (Scalar) line=5 col=7
          Sequence line=5 col=22
```

So the key node for the typo is at `line=5 col=7` — the column is available from the node tree
but not from the `TypeError`. Note that decoding into a `Node` is a **separate pass**: the strict
`Decode` into the struct does not hand you the node tree.

### 1.5 Strict decoding of a sub-node: the heterogeneous-step model (`with:`)

If the step list is heterogeneous, the natural model is `Type string` plus `With yaml.Node`
(or `With map[string]any`), with the `with` payload decoded afterwards into the step type's own
struct (e.g. `ScanWith{Roots, Extensions}`). Measured on `/tmp/yamlnode/main.go`:

```yaml
steps:
  - type: scrape
    with:
      roots: [/mnt/a]
      extensoins: [mkv]     # line 5
```

| Step | Result (verbatim) |
| --- | --- |
| `With map[string]any` + `KnownFields(true)` on the outer document | `err = <nil>` — typo silently accepted (same map limitation as §1.3) |
| `With yaml.Node` + `KnownFields(true)` on the outer document | `err = <nil>`; the node is copied verbatim |
| `yaml.Marshal(&node)`, then strict-decode the snippet into `ScanWith` | `yaml: unmarshal errors:\n  line 2: field extensoins not found in type main.ScanWith` — **line 2 of the re-encoded snippet, not line 5 of the file** |
| same, but the snippet holds only the bad key | `yaml: unmarshal errors:\n  line 1: field extensoins not found in type main.ScanWith` |
| `(*yaml.Node).Decode(&ScanWith{})` (no re-encode) | `err = <nil>   decoded = {Roots:[/mnt/a] Extensions:[]}` |
| baseline: `With ScanWith` (a real struct) | `yaml: unmarshal errors:\n  line 5: field extensoins not found in type main.ScanWith` — correct |

Verbatim output:

```
=== (a) outer strict decode with `With yaml.Node` ===
err = <nil>
step0.With.Kind = 4 (4 == MappingNode), step0.With.Line = 4

=== (b) re-marshal the node, then strict-decode the snippet ===
re-marshalled snippet = "roots: [/mnt/a]\nextensoins: [mkv]\n"
err = yaml: unmarshal errors:
  line 2: field extensoins not found in type main.ScanWith
snippet with only the bad key = "extensoins: [mkv]\n" -> err = yaml: unmarshal errors:
  line 1: field extensoins not found in type main.ScanWith

=== (c) (*yaml.Node).Decode (no re-encode, no way to make it strict) ===
err = <nil>   decoded = {Roots:[/mnt/a] Extensions:[]}

=== (d) baseline: same doc with `with` typed as a struct (correct line) ===
err = yaml: unmarshal errors:
  line 5: field extensoins not found in type main.ScanWith
```

So a `yaml.Node`/`map[string]any` payload defeats `KnownFields` entirely, and re-encoding the node
before a strict decode yields a line number relative to the **re-encoded snippet** (which starts at
line 1), not to the config file. The reported line is the offending key's position *inside that
snippet*: it is `line 2` when another key is re-encoded before it, and `line 1` when the snippet
starts with the offending key — the same file position (line 5) in both cases. `step0.With.Line = 4` confirms the copied node still knows its own
position in the original document (the mapping node's line is its first key's line).

The node copy is verbatim — `gopkg.in/yaml.v3@v3.0.1/decode.go:492-494`:

```go
	if out.Type() == nodeType {
		out.Set(reflect.ValueOf(n).Elem())
		return true
	}
```

### 1.6 How to get line-accurate unknown-key errors for a sub-node

**Can `(*yaml.Node).Decode` be made strict or position-preserving? No.**

Signature — `gopkg.in/yaml.v3@v3.0.1/yaml.go:142-154`:

```go
func (n *Node) Decode(v interface{}) (err error) {
	d := newDecoder()
	defer handleErr(&err)
	out := reflect.ValueOf(v)
	if out.Kind() == reflect.Ptr && !out.IsNil() {
		out = out.Elem()
	}
	d.unmarshal(n, out)
	if len(d.terrors) > 0 {
		return &TypeError{d.terrors}
	}
	return nil
}
```

- There is no strict parameter and no `KnownFields` equivalent on `Node`. `knownFields` is a field of
  the unexported `decoder` (`decode.go:321 knownFields bool`), settable only through
  `(*Decoder).KnownFields` (`yaml.go:110`), which `Node.Decode` never touches — `newDecoder()` leaves
  it at the zero value (`decode.go`: `func newDecoder() *decoder { d := &decoder{ stringMapType: stringMapType, generalMapType: generalMapType, uniqueKeys: true } ... }`).
- The unexported helper `func unmarshal(in []byte, out interface{}, strict bool)` (`yaml.go:156`)
  declares a `strict` argument that is **never referenced in the body**:
  `grep -n "strict" gopkg.in/yaml.v3@v3.0.1/yaml.go gopkg.in/yaml.v3@v3.0.1/decode.go` →
  the single match is `yaml.go:156:func unmarshal(in []byte, out interface{}, strict bool) (err error) {`.
  So `yaml.Unmarshal` cannot be made strict either.
- Error positions therefore always come from the node's own `Line`/`Column` (`decode.go:943` uses
  `ni.Line`), which are correct **only while the node still carries the original document's
  positions** — re-encoding discards them (§1.5).

Two workable techniques, both measured, both printing the true `line 5 col 7` for the document in
§1.5:

- **(a)** walk the `yaml.Node` you already hold for `with:` and compare key names against the step
  struct's `yaml` tags; report `keyNode.Line` / `keyNode.Column`. No second parse.
- **(b)** decode the whole file into a `yaml.Node` tree first, drive the expected type from the step's
  `type:` discriminator, and validate key names by walking the tree before/alongside decoding.

Measured output (`/tmp/yamlnode/main.go`):

```
=== (e) technique 1: walk the already-decoded `With yaml.Node` (positions preserved) ===
  line 5 col 7: field extensoins not found in type main.ScanWith

=== (f) technique 2: decode the whole file into a Node tree, validate by walking ===
  line 5 col 7: field extensoins not found in type main.ScanWith
```

Both give the exact position of the offending key (`extensoins` is at line 5, column 7), where the
re-encode-and-decode route reported `line 2` (`line 1` for the trimmed snippet) for the same file.

Technique (a)'s core (full source in Appendix A.5):

```go
func checkStruct(n *yaml.Node, t reflect.Type, path string) []problem {
	var out []problem
	switch n.Kind {
	case yaml.MappingNode:
		if t == nil || t.Kind() != reflect.Struct {
			return out
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Tag == "!!merge" || k.Value == "<<" {
				continue // merge keys are expanded by the decoder, not struct fields
			}
			f, ok := fieldByTag(t, k.Value)
			if !ok {
				out = append(out, problem{k.Line, k.Column, path,
					fmt.Sprintf("field %s not found in type %s", k.Value, t)})
				continue
			}
			out = append(out, checkStruct(v, deref(f.Type), path+"."+k.Value)...)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			out = append(out, checkStruct(c, t, path)...)
		}
	}
	return out
}
```

`fieldByTag` mirrors yaml.v3's own tag handling (the part of the `yaml` tag before the first comma,
falling back to the lower-cased field name). Note the walker must skip `<<` keys itself, because a
merged key is not a struct field name (§3.4).

### 1.7 Maintenance status of `gopkg.in/yaml.v3` (fact, not a judgement)

The upstream repo `go-yaml/yaml` is **archived** on GitHub, and its README (branch `v3`) starts with:

> `# THIS PROJECT IS UNMAINTAINED`

Source: <https://raw.githubusercontent.com/go-yaml/yaml/v3/README.md> (fetched 2026-10-27).
GitHub API `GET /repos/go-yaml/yaml` → `"archived": true`, `"pushed_at": "2025-04-01T17:00:11Z"`.
Latest tag `v3.0.1` points at commit `f6f7691b1f`, commit date `2022-05-27T08:35:30Z`.
`gopkg.in/yaml.v3` remains the newest published version of that module path
(`go list -m -versions gopkg.in/yaml.v3` → `v3.0.0 v3.0.1`).

---

## 2. Strict decoding with line numbers (TOML)

### 2.1 `github.com/pelletier/go-toml/v2` v2.4.3 — yes, with a strict flag and full positions

Strict mode is opt-in on the `*Decoder` — `go-toml/v2@v2.4.3/unmarshaler.go:95-105`:

```go
// DisallowUnknownFields causes the Decoder to return an error when the
// destination is a struct and the input contains a key that does not match a
// non-ignored field.
//
// In that case, the Decoder returns a StrictMissingError that can be used to
// retrieve the individual errors as well as generate a human readable
// description of the missing fields.
func (d *Decoder) DisallowUnknownFields() *Decoder {
	d.strict = true
	return d
}
```

Test doc (`/tmp/tomltest/main.go`), typo inside a list element:

```toml
name = "demo"

[[steps]]
type = "scrape"

[steps.with]
plot_prioirty = []
```

Default (non-strict) `toml.Unmarshal`: `err = <nil>` — the typo is silently ignored.

Strict mode raw output:

```
=== pelletier DisallowUnknownFields ===
err type = *toml.StrictMissingError
err = strict mode: fields in the document are missing in the target struct

contains "line": false
is *toml.StrictMissingError = true
sme.String() = 4| type = "scrape"
5|
6| [steps.with]
7| plot_prioirty = []
 | ~~~~~~~~~~~~~ unknown field
sme.Error() = strict mode: fields in the document are missing in the target struct
len(sme.Errors) = 1
  Errors[0]: Key=[steps with plot_prioirty] Position=(row=7,col=1) Error()="toml: unknown field" String()="4| type = \"scrape\"\n5|\n6| [steps.with]\n7| plot_prioirty = []\n | ~~~~~~~~~~~~~ unknown field"
```

Facts:
- `err.(*toml.StrictMissingError).Error()` is the **generic** sentence
  `strict mode: fields in the document are missing in the target struct` and contains no position.
- The per-field detail is `StrictMissingError.Errors []DecodeError` (`go-toml/v2@v2.4.3/errors.go:31-34`):
  ```go
  type StrictMissingError struct {
  	// One error per field that could not be found.
  	Errors []DecodeError
  }
  ```
- `DecodeError` exposes `Position() (row, column int)` (1-indexed) and `Key() Key`
  (`errors.go:82-89`), plus a human-readable `String()` with the offending line and a `~~~~` underline.
- Here the position is `row=7 col=1` and the key path is `[steps with plot_prioirty]` — so both the
  **line and column** of the offending key are available programmatically.
- `StrictMissingError.String()` concatenates all `DecodeError.String()` values separated by `\n---\n`
  (`errors.go:42-55`).
- A missing whole table is reported with message `"missing table"` (`go-toml/v2@v2.4.3/strict.go:43-52`).
- Parse (syntax) errors also carry positions — `*toml.DecodeError`:
  ```
  pelletier syntax err type = *toml.DecodeError
  err = toml: array is incomplete
  pelletier Position() = row 2 col 14
  ```
- License: MIT (`go-toml/v2@v2.4.3/LICENSE`: `The MIT License (MIT)` / `go-toml v2`).
- Maintenance: not archived; GitHub API `GET /repos/pelletier/go-toml` → `"archived": false`,
  `"pushed_at": "2026-08-03T12:43:48Z"`, `"license": {"spdx_id": "NOASSERTION"}` (the LICENSE file is
  a custom-formatted MIT text, hence `NOASSERTION`), latest commits 2026-07-18.

### 2.2 `github.com/BurntSushi/toml` v1.6.0 — no strict flag; unknown keys via `MetaData.Undecoded()`

Raw output:

```
=== BurntSushi Decode ===
err = <nil>
md.Undecoded() = [steps.with.plot_prioirty]
md.IsDefined("steps","with","plot_prioirty") = false
md.IsDefined("steps","with","plot_priority") = false
```

- `toml.Decode` returns `err = <nil>` for the typo'd key; nothing is reported as an error.
- Unknown keys are discovered afterwards from the returned `MetaData` — `BurntSushi/toml@v1.6.0/meta.go:71-82`:
  ```go
  // Undecoded returns all keys that have not been decoded in the order in which
  // they appear in the original TOML document.
  ...
  func (md *MetaData) Undecoded() []Key {
  ```
  The result is a list of `Key` (a key path) with **no position information**.
- The library documents this as intentional — `BurntSushi/toml@v1.6.0/decode.go:113-115`:
  ```go
  // The mapping between TOML values and Go values is loose. ...
  // This loose mapping can be made stricter by using the IsDefined and/or
  // Undecoded methods on the MetaData returned.
  ```
- A grep of the non-test sources for a strict flag finds only that doc comment:
  `grep -rn "DisallowUnknownFields\|strict" BurntSushi/toml@v1.6.0/*.go` → only
  `decode.go:115` (the comment above) and an unrelated `parse.go:45` comment.
  `toml.NewDecoder(r)` returns a `*toml.Decoder` with only unexported fields (`r io.Reader`).
- Syntax errors **do** carry a line number: `BurntSushi syntax err = toml: line 2 (last key "steps"): expected a comma (',') or array terminator (']'), but got end of file`.
- License: MIT; not archived; `"pushed_at": "2026-08-18T20:04:59Z"`.

### 2.3 Unknown keys in nested sub-tables, and the map limitation

`/tmp/tomlfollow/main.go`; the document has `extensoins` on line 8, inside the `[steps.with]`
sub-table of an array-of-tables:

```
=== (a) pelletier strict, `with` as map[string]any (heterogeneous step) ===
err = <nil>

=== (b) pelletier strict, `with` as a struct ===
err = strict mode: fields in the document are missing in the target struct
err type = *toml.StrictMissingError
  Errors[0]: Key=[steps with extensoins] Position=(row=8,col=1) Error()="toml: unknown field"
  String() =
6| [steps.with]
7| roots = ["/mnt/a"]
8| extensoins = ["mkv"]
 | ~~~~~~~~~~ unknown field

=== (c) BurntSushi, same doc ===
err = <nil>
md.Undecoded() = [steps.with.extensoins]
md.Keys() = [name steps steps.type steps.with steps.with.roots steps.with.extensoins]
```

Facts:
- `Position()` returns row **and** column of the unknown key, and it works for a nested sub-table of
  an array-of-tables — so pelletier reports positions for **unknown keys**, not only for syntax
  errors (contrast §2.1's `*toml.DecodeError` for syntax).
- A `map[string]any` field is not checked even under `DisallowUnknownFields()` — the same map blind
  spot as yaml.v3 (§1.3, §1.5).
- `BurntSushi/toml` v1.6.0 exposes no positions for unknown keys: `MetaData.Undecoded()` returns
  `[steps.with.extensoins]` (key paths) and `Key` is a `[]string`
  (`BurntSushi/toml@v1.6.0/meta.go:96 func (k Key) String() string`).

### 2.4 Summary of question 2

| target field type | strict flag | rejects unknown keys | line in the error | column in the error |
| --- | --- | --- | --- | --- |
| `pelletier/go-toml/v2` v2.4.3, struct | `Decoder.DisallowUnknownFields()` | yes, `*StrictMissingError` | yes (`DecodeError.Position()`, and in `String()`) | yes (`Position()`) |
| `pelletier/go-toml/v2` v2.4.3, `map[string]any` | same | no — `err = <nil>` | n/a | n/a |
| `BurntSushi/toml` v1.6.0, struct | none | no error; `MetaData.Undecoded()` lists key paths | no | no |

---

## 3. YAML anchors and merge keys (`gopkg.in/yaml.v3` v3.0.1)

### 3.1 Support confirmed

Both anchors (`&name`), aliases (`*name`) and the `<<:` merge key are supported. The merge key is
resolved by the resolver — `gopkg.in/yaml.v3@v3.0.1/resolve.go:59` and `:80`:

```go
		{"<<", mergeTag, []string{"<<"}},
```
```go
	mergeTag     = "!!merge"
```

and detected during decoding — `gopkg.in/yaml.v3@v3.0.1/decode.go:998-1000`:

```go
func isMerge(n *Node) bool {
	return n.Kind == ScalarNode && n.Value == "<<" && (n.Tag == "" || n.Tag == "!" || shortTag(n.Tag) == mergeTag)
}
```

In the node tree the merge key appears as a scalar with tag `!!merge`:

```
         kind=Scalar tag="!!merge" value="<<" line=7 col=5 alias=0x0
         kind=Alias tag="" value="d" line=7 col=9 alias=0x273fbabf54a0
```

### 3.2 Merge keys do **not** trip `KnownFields` by themselves

Test 1 (`/tmp/yamlanchor/main.go`), merge key with all keys known:

```yaml
defaults: &d
  type: scrape
  with:
    plot_priority: ["a"]
name: demo
steps:
  - <<: *d
    type: organize
```

```
=== 1: merge key <<: *d, all keys known ===
err = <nil>

decoded p1 = {Defaults:{Type:scrape With:{PlotPriority:[a] Plot:}} Name:demo Steps:[{Type:organize With:{PlotPriority:[a] Plot:}}]}
```

So `KnownFields(true)` does not treat `<<` itself as an unknown field, and the merged values are
applied (the explicit `type: organize` in the list element wins over the merged `type: scrape`).

### 3.3 Interaction results (all four cases, verbatim)

| # | Document | Result |
| --- | --- | --- |
| 1 | merge key, all keys known | `err = <nil>` |
| 2 | merge key **plus** a typo'd key in the same mapping (`tpye`, line 8) | `yaml: unmarshal errors:\n  line 8: field tpye not found in type main.Step` |
| 3 | plain alias without merge key (`with: *d`, where `*d` is a mapping with keys `type`/`with`) | `yaml: unmarshal errors:\n  line 2: field type not found in type main.StepWith\n  line 3: field with not found in type main.StepWith` |
| 4 | unknown key `bogus_field` lives in the **anchored** mapping and is pulled in via `<<` | `yaml: unmarshal errors:\n  line 2: field bogus_field not found in type main.Step\n  line 2: field bogus_field not found in type main.Step` (the same error **twice**) |

Case 3 decoded result shows the alias semantics gotcha:
`decoded p3 = {Defaults:{Type:scrape With:{PlotPriority:[a] Plot:}} Name:demo Steps:[{Type:scrape With:{PlotPriority:[] Plot:}}]}` —
the alias was substituted as the *whole value* of `with`, so its keys (`type`, `with`) were matched
against `StepWith`.

Two concrete observations for strict-mode error reporting:
- When an unknown key is only reachable through an anchor/merge, the reported line is the line of
  the **anchor definition**, not the line of the `<<`/alias usage (case 3 → lines 2/3; case 4 → line 2).
- Case 4 emits the same message twice (the merged mapping is decoded once for `defaults` and once
  for the `steps` element), so error lists can contain duplicates.

`gopkg.in/yaml.v3@v3.0.1/decode.go:959-995` implements the merge (`func (d *decoder) merge(...)`) and
`decode.go:955` has the failure mode:

```go
	failf("map merge requires map or sequence of maps as the value")
```

### 3.4 Merge keys do not bypass strict decoding (step-to-step merge)

Independent test: the anchor is defined on one step and merged into a *different* step, with `with`
typed as a real struct (`/tmp/yamlnode/main.go`):

```yaml
steps:
  - &base
    type: scrape
    with:
      roots: [/mnt/a]
      bogus_key: 1        # line 6
  - <<: *base             # line 7 — the merge site
    type: organize
```

```
=== (g) merge key into another step, `with` as a struct ===
err = yaml: unmarshal errors:
  line 6: field bogus_key not found in type main.ScanWith
  line 6: field bogus_key not found in type main.ScanWith
decoded = [{Type:scrape With:{Roots:[/mnt/a] Extensions:[]}} {Type:organize With:{Roots:[/mnt/a] Extensions:[]}}]
```

```
=== (h) `<<` itself is never reported as unknown ===
err = <nil>
decoded = [{Type:scrape With:{Roots:[/mnt/a] Extensions:[]}} {Type:organize With:{Roots:[/mnt/a] Extensions:[]}}]
```

Mechanism, all in `gopkg.in/yaml.v3@v3.0.1/decode.go`:
- The `<<` key is skipped **before** the known-fields check, so it is never reported as unknown —
  `decode.go:905-910`:
  ```go
	for i := 0; i < l; i += 2 {
		ni := n.Content[i]
		if isMerge(ni) {
			mergeNode = n.Content[i+1]
			continue
		}
  ```
- The merge is then decoded through the **same** decoder, so `d.knownFields` is still in force —
  `decode.go:948-951`:
  ```go
	d.mergedFields = mergedFields
	if mergeNode != nil {
		d.merge(n, mergeNode, out)
	}
  ```
  and `merge` calls `d.unmarshal(merge, out)` (`decode.go:959-995`), which re-enters the struct
  mapping path that emits the unknown-field error at `decode.go:943`.

Consequences measured above: the error is reported at the **anchor definition** line (6), not at the
merge line (7), and it appears **twice** (once for the step that defines the anchor, once for the
step that merges it). Both were already visible in §3.3 cases 3 and 4.

**Can `<<:` be allowed without weakening the unknown-key guarantee?** On this evidence, yes for
struct-typed targets: a merged mapping goes through exactly the same known-fields check as an inline
one, and `<<` itself is exempt from that check by construction. The unknown-key guarantee is
weakened only by map-typed targets (§1.3, §1.5, §2.3), which is independent of merge keys. Two
reporting caveats come with allowing `<<:` — positions point at the anchor definition rather than the
merge site, and duplicate error entries appear.

---

## 4. `github.com/nikolalohinski/gonja/v2`

### 4.1 Module path, version, maintenance, license

- Module path: `github.com/nikolalohinski/gonja/v2` (v2 major version in the path).
  `go get github.com/nikolalohinski/gonja/v2@latest` → `v2.9.1`.
  `go list -m -versions github.com/nikolalohinski/gonja/v2` ends with
  `... v2.7.0 v2.8.0 v2.9.0 v2.9.1`.
- License: MIT. `gonja/v2@v2.9.1/LICENSE`:
  ```
  The MIT License (MIT)

  Copyright (c) 2013-2014 Florian Schlachter
  ```
  GitHub API `GET /repos/nikolalohinski/gonja` → `"license": {"spdx_id": "MIT"}`.
- Maintenance: not archived, `"archived": false`, `"pushed_at": "2026-10-12T05:09:47Z"`,
  `"stargazers_count": 173`. Latest commits at fetch time:
  ```
  2026-10-09T22:07:36Z Fix iteration over custom sequences
  2026-10-08T08:46:10Z fix: support exec.Dict in Value.Items()
  2026-09-22T21:56:30Z parser: add structured SyntaxError type
  ```
  The GitHub Releases API returned **no entries** (`GET /repos/nikolalohinski/gonja/releases` → `[]`),
  but version tags exist (`v2.9.1 cc18d35e`, `v2.9.0 03d9b27f`, …).
- Self-description (`gonja/v2@v2.9.1/README.md`):
  > `gonja` is a pure `go` implementation of the [Jinja template engine](https://jinja.palletsprojects.com/). It aims to be as compatible as possible with the original `python` implementation.

### 4.2 Auto-escaping: the switch

`gonja/v2@v2.9.1/config/config.go:18-22`:

```go
	// If set to True the XML/HTML autoescaping feature is enabled by default.
	// For more details about autoescaping see Markup.
	// This can also be a callable that is passed the template name
	// and has to return True or False depending on autoescape should be enabled by default.
	AutoEscape bool
```

Default is **off** — `config/config.go:40-48`:

```go
func New() *Config {
	return &Config{
		...
		AutoEscape:          false,
```

It is consumed at render time — `gonja/v2@v2.9.1/exec/renderer.go:106-111`:

```go
		if r.Config.AutoEscape && value.IsString() && !value.Safe {
			_, err = io.WriteString(r.Output, value.Escaped())
		} else {
			_, err = io.WriteString(r.Output, value.String())
		}
```

The escaping itself is HTML/XML escaping plus an explicit apostrophe rule —
`gonja/v2@v2.9.1/utils/escape.go:9-13`:

```go
func Escape(in string) string {
	output := html.EscapeString(in)
	output = strings.ReplaceAll(output, "'", "&#39;")
	return output
}
```

(`exec/value.go:382-384`: `func (v *Value) Escaped() string { return u.Escape(v.String()) }`.)

Measured escaping set (from the test below): `&` → `&amp;`, `<` → `&lt;`, `>` → `&gt;`,
`"` → `&#34;`, `'` → `&#39;`.

Also available:
- a `safe` filter that marks a value as not-to-be-escaped (`builtins/filters.go:64` `"safe": filterSafe`;
  `exec/value.go:47-52` `func AsSafeValue(i any) *Value` sets `Safe: true`);
- a per-template-block override, `{% autoescape true %}…{% endautoescape %}`
  (`builtins/control_structures/autoescape.go:25-27` sets `sub.Config.AutoEscape = acs.Autoescape`),
  documented at `gonja/v2@v2.9.1/docs/control_structures.md:221`:
  > `## The `autoescape` control structure`
  > `If you want you can activate and deactivate the autoescaping from within the templates.`

### 4.3 Per-environment escaping: possible, because the config is passed per template

`exec.NewTemplate` takes the config as a parameter — `gonja/v2@v2.9.1/exec/template.go:29`:

```go
func NewTemplate(identifier string, config *config.Config, loader loaders.Loader, environment *Environment) (*Template, error) {
```

so two `config.Config` values with different `AutoEscape` can be used independently. Measured with
two configs in one process (`/tmp/gonjatest/main.go`, data `title = A & B <C> "quoted"`,
`path = /media/A & B <C>/movie.nfo`):

```
$ go run .
AutoEscape=true  -> "<title>A &amp; B &lt;C&gt; &#34;quoted&#34;</title>\n<path>/media/A &amp; B &lt;C&gt;/movie.nfo</path>" err=<nil>
AutoEscape=false -> "/media/A & B <C>/movie.nfo" err=<nil>
config.New().AutoEscape = false
gonja.DefaultConfig.AutoEscape = false (package-level global)
autoescape-override in raw env -> "A &amp; B &lt;C&gt; &#34;quoted&#34;|A & B <C> \"quoted\"" err=<nil>
gonja.FromString -> "/media/A & B <C>/movie.nfo" err=<nil>
```

i.e. an escaping environment (HTML/XML escaping — the NFO-shaped case) and a raw environment
(no escaping — the file-path case) coexisted in the same process, and `{% autoescape true %}`
inside the raw environment still escaped only its own block.

**Caveat (global mutable state):** the convenience constructors `gonja.FromString` / `FromBytes` /
`FromFile` use the package-level mutable `gonja.DefaultConfig` — `gonja/v2@v2.9.1/gonja.go:20-31`
and `:47-69`:

```go
	DefaultConfig      = config.New()
```
```go
	return exec.NewTemplate(rootID, DefaultConfig, shiftedLoader, DefaultEnvironment)
```

Mutating it changes every `FromString`/`FromFile` call process-wide. Measured
(`/tmp/gonjatest/global_test.go`):

```
DefaultConfig.AutoEscape=false: "A & B"
after gonja.DefaultConfig.AutoEscape=true: "A &amp; B"
```

`gonja.DefaultEnvironment` (`gonja.go:22-28`) is likewise a package-level shared value; the
`config.Config` is not part of `exec.Environment` (`exec/environment.go:13-19` lists
`Filters`, `ControlStructures`, `Tests`, `Context`, `Methods` only), so per-environment escaping is
expressed by passing a different `*config.Config` to `exec.NewTemplate`, not by the `Environment`.

---

## 5. Reload detection

- `gopkg.in/yaml.v3` offers nothing notable for detecting a changed file: its exported surface is
  `Unmarshal` / `Marshal` / `Decoder` / `Encoder` / `Node` (`yaml.go`; `grep -rn "sha256\|fsnotify\|Watch\|hash" gopkg.in/yaml.v3@v3.0.1/*.go` → no matches). Content hashing would come from
  `crypto/sha256` in the stdlib, i.e. it is not a library question.
- `fsnotify` is documented as unable to work over NFS — `fsnotify/fsnotify` README (fetched 2026-10-27,
  <https://raw.githubusercontent.com/fsnotify/fsnotify/main/README.md>):
  > `### Why don't notifications work with NFS, SMB, FUSE, /proc, or /sys?`
  > `fsnotify requires support from underlying OS to work. The current NFS and SMB protocols does not provide network level support for file notifications`
  > `This could be fixed with a polling watcher ([#9]), but it's not yet implemented.`
  (latest version `v1.10.1`; GitHub API `GET /repos/fsnotify/fsnotify` → `"license": {"spdx_id": "BSD-3-Clause"}`, `"pushed_at": "2026-05-11T11:23:52Z"`).
- The old repo's own decision, `/root/clone/open-mdcng/docs/adr/0008-config-file-authoritative.md:16`
  (file content, Chinese): `不装文件监视——目标环境是 NFS，无 inotify，装了也是假信号。` and `:19`:
  `配置的「变更」用内容指纹（config.toml + rules/* 的 hash）判定，不用 mtime（NFS 上粒度与时钟偏移都不可靠）`.

---

## Appendix A: exact test programs

### A.1 `/tmp/yamltest` — `go mod init x`, `go get gopkg.in/yaml.v3`

`main.go` (relevant parts):

```go
type StepWith struct {
	PlotPriority []string `yaml:"plot_priority"`
	Plot         string   `yaml:"plot,omitempty"`
}

type Step struct {
	Type string   `yaml:"type"`
	With StepWith `yaml:"with,omitempty"`
}

type Pipeline struct {
	Name  string `yaml:"name"`
	Steps []Step `yaml:"steps"`
}

const docA = `name: demo
steps:
  - type: scrape
    with:
      plot_prioirty: []
`

const docB = `name: demo
steps:
  - tpye: scrape
`

const docC = `nome: demo
steps: []
`

func strict(doc string, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader([]byte(doc)))
	dec.KnownFields(true)
	return dec.Decode(v)
}
```

`extra_test.go`:

```go
type Sub struct {
	PlotPriority []string `yaml:"plot_priority"`
}

type StepE struct {
	Type string `yaml:"type"`
	With Sub    `yaml:"with"`
}

type P1 struct {
	Steps []StepE `yaml:"steps"`
}

type P2 struct {
	Steps []struct {
		Type string         `yaml:"type"`
		With map[string]Sub `yaml:"with"`
	} `yaml:"steps"`
}

func TestMulti(t *testing.T) {
	doc := "steps:\n  - type: a\n    with:\n      nope: 1\n  - type: b\n    with:\n      nada: 2\n"
	var p P1
	dec := yaml.NewDecoder(bytes.NewReader([]byte(doc)))
	dec.KnownFields(true)
	err := dec.Decode(&p)
	fmt.Printf("two unknown keys err = %q\n", err)

	doc2 := "steps:\n  - type: a\n    with:\n      anything_goes:\n        plot_prioirty: []\n"
	var p2 P2
	dec2 := yaml.NewDecoder(bytes.NewReader([]byte(doc2)))
	dec2.KnownFields(true)
	err2 := dec2.Decode(&p2)
	fmt.Printf("map[string]Sub, free map key + typo'd inner field err = %q\n", err2)
}
```

### A.2 `/tmp/yamlanchor` — `go mod init y`, `go get gopkg.in/yaml.v3`

Same `StepWith`/`Step`/`Pipeline` types (`Pipeline` additionally has `Defaults Step \`yaml:"defaults,omitempty"\``),
plus these documents:

```go
const docMergeOK = `defaults: &d
  type: scrape
  with:
    plot_priority: ["a"]
name: demo
steps:
  - <<: *d
    type: organize
`

const docMergeUnknown = `defaults: &d
  type: scrape
  with:
    plot_priority: ["a"]
name: demo
steps:
  - <<: *d
    tpye: organize
`

const docAlias = `defaults: &d
  type: scrape
  with:
    plot_priority: ["a"]
name: demo
steps:
  - type: scrape
    with: *d
`

const docMergeBringsUnknown = `defaults: &d
  bogus_field: 1
name: demo
steps:
  - <<: *d
`
```

### A.3 `/tmp/tomltest` — `go mod init t`, `go get github.com/BurntSushi/toml@latest github.com/pelletier/go-toml/v2@latest`

`main.go` (relevant parts) and `parse_test.go`:

```go
type StepWith struct {
	PlotPriority []string `toml:"plot_priority"`
}

type Step struct {
	Type string   `toml:"type"`
	With StepWith `toml:"with"`
}

type Pipeline struct {
	Name  string `toml:"name"`
	Steps []Step `toml:"steps"`
}

const doc = `name = "demo"

[[steps]]
type = "scrape"

[steps.with]
plot_prioirty = []
`

// strict:
dec := ptoml.NewDecoder(bytes.NewReader([]byte(doc)))
dec.DisallowUnknownFields()
err2 := dec.Decode(&p2)
// ...
row, col := de.Position()
fmt.Printf("  Errors[%d]: Key=%v Position=(row=%d,col=%d) Error()=%q String()=%q\n",
	i, de.Key(), row, col, de.Error(), de.String())

// BurntSushi:
md, err3 := btoml.Decode(doc, &p3)
fmt.Printf("md.Undecoded() = %v\n", md.Undecoded())
```

```go
const badDoc = "name = \"demo\"\nsteps = [1, 2\n"

func TestParseErrors(t *testing.T) {
	var v map[string]any
	_, err := btoml.Decode(badDoc, &v)
	fmt.Printf("BurntSushi syntax err = %v\n", err)
	err2 := ptoml.Unmarshal([]byte(badDoc), &v)
	fmt.Printf("pelletier syntax err type = %T\nerr = %v\n", err2, err2)
	if de, ok := err2.(*ptoml.DecodeError); ok {
		r, c := de.Position()
		fmt.Printf("pelletier Position() = row %d col %d\n", r, c)
		fmt.Printf("pelletier String() =\n%s\n", de.String())
	}
}
```

### A.4 `/tmp/gonjatest` — `go mod init g`, `go get github.com/nikolalohinski/gonja/v2@latest`

```go
func render(cfg *config.Config, src string, ctx *exec.Context) (string, error) {
	loader, err := loaders.NewFileSystemLoader("")
	if err != nil {
		return "", err
	}
	shifted, err := loaders.NewShiftedLoader("tpl", bytes.NewReader([]byte(src)), loader)
	if err != nil {
		return "", err
	}
	tpl, err := exec.NewTemplate("tpl", cfg, shifted, gonja.DefaultEnvironment)
	if err != nil {
		return "", err
	}
	return tpl.ExecuteToString(ctx)
}

func main() {
	title := `A & B <C> "quoted"`
	pathVal := `/media/A & B <C>/movie.nfo`

	nfoCfg := config.New()
	nfoCfg.AutoEscape = true

	rawCfg := config.New()
	rawCfg.AutoEscape = false

	ctx := exec.NewContext(map[string]any{"title": title, "path": pathVal})
	// render `<title>{{ title }}</title>` with nfoCfg, `{{ path }}` with rawCfg, etc.
}
```

### A.5 `/tmp/yamlnode` — `go mod init n`, `go get gopkg.in/yaml.v3`

Heterogeneous step model and the two line-accurate techniques:

```go
type ScanWith struct {
	Roots      []string `yaml:"roots"`
	Extensions []string `yaml:"extensions"`
}

type OrganizeWith struct {
	Template string `yaml:"template"`
}

var stepWithTypes = map[string]reflect.Type{
	"scrape":   reflect.TypeOf(ScanWith{}),
	"organize": reflect.TypeOf(OrganizeWith{}),
}

type RawStep struct {
	Type string    `yaml:"type"`
	With yaml.Node `yaml:"with"`
}

type Pipeline struct {
	Steps []RawStep `yaml:"steps"`
}

const doc = `steps:
  - type: scrape
    with:
      roots: [/mnt/a]
      extensoins: [mkv]
`

type Step2 struct {
	Type string   `yaml:"type"`
	With ScanWith `yaml:"with"`
}

type Pipeline2 struct {
	Steps []Step2 `yaml:"steps"`
}

const docMerge = `steps:
  - &base
    type: scrape
    with:
      roots: [/mnt/a]
      bogus_key: 1
  - <<: *base
    type: organize
`
```

`checkStruct` (technique 1) is quoted in §1.6; `walkSteps` (technique 2) resolves the `with` type
from the `type:` discriminator and calls the same `checkStruct` on the `with:` node:

```go
func walkSteps(n *yaml.Node) []problem {
	var out []problem
	var find func(*yaml.Node, string) *yaml.Node
	find = func(m *yaml.Node, key string) *yaml.Node {
		if m.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				return m.Content[i+1]
			}
		}
		return nil
	}
	steps := find(n.Content[0], "steps")
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return out
	}
	for i, s := range steps.Content {
		tv := find(s, "type")
		w := find(s, "with")
		if tv == nil || w == nil {
			continue
		}
		t := stepWithTypes[tv.Value]
		out = append(out, checkStruct(w, t, fmt.Sprintf("steps[%d](%s).with", i, tv.Value))...)
	}
	return out
}
```

`fieldByTag`:

```go
func fieldByTag(t reflect.Type, key string) (reflect.StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		if name == key {
			return f, true
		}
	}
	return reflect.StructField{}, false
}
```

### A.6 `/tmp/tomlfollow` — `go mod init tf`, `go get github.com/BurntSushi/toml@latest github.com/pelletier/go-toml/v2@latest`

```go
type ScanWith struct {
	Roots      []string `toml:"roots"`
	Extensions []string `toml:"extensions"`
}

type RawStep struct {
	Type string         `toml:"type"`
	With map[string]any `toml:"with"`
}

type RawPipeline struct {
	Name  string    `toml:"name"`
	Steps []RawStep `toml:"steps"`
}

type Step struct {
	Type string   `toml:"type"`
	With ScanWith `toml:"with"`
}

type Pipeline struct {
	Name  string `toml:"name"`
	Steps []Step `toml:"steps"`
}

const doc = `name = "demo"

[[steps]]
type = "scrape"

[steps.with]
roots = ["/mnt/a"]
extensoins = ["mkv"]
`
```

`go.mod`:

```
module tf

go 1.27.2

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/pelletier/go-toml/v2 v2.4.3
)
```

---

## Appendix B: raw command output

### B.1 `/tmp/yamltest` — `$ go run .`

```
=== A: unknown key inside list-element struct field (steps[].with.plot_prioirty) ===
err type = *yaml.TypeError
err string = yaml: unmarshal errors:
  line 5: field plot_prioirty not found in type main.StepWith
contains "line": true
is *yaml.TypeError = true, len(Errors) = 1
  Errors[0] = "line 5: field plot_prioirty not found in type main.StepWith"

=== B: typo of list-element struct field (steps[].tpye) ===
err type = *yaml.TypeError
err string = yaml: unmarshal errors:
  line 3: field tpye not found in type main.Step
contains "line": true
is *yaml.TypeError = true, len(Errors) = 1
  Errors[0] = "line 3: field tpye not found in type main.Step"

=== C: typo at root (nome) ===
err type = *yaml.TypeError
err string = yaml: unmarshal errors:
  line 1: field nome not found in type main.Pipeline
contains "line": true
is *yaml.TypeError = true, len(Errors) = 1
  Errors[0] = "line 1: field nome not found in type main.Pipeline"

=== D: unknown key inside map[string]any (steps[].with.plot_prioirty) ===
err = <nil>
=== yaml.v3 version ===
see go.mod
=== node tree line/col for docA ===
Document line=1 col=1
  Mapping line=1 col=1
    "name" (Scalar) line=1 col=1
    "demo" (Scalar) line=1 col=7
    "steps" (Scalar) line=2 col=1
    Sequence line=3 col=3
      Mapping line=3 col=5
        "type" (Scalar) line=3 col=5
        "scrape" (Scalar) line=3 col=11
        "with" (Scalar) line=4 col=5
        Mapping line=5 col=7
          "plot_prioirty" (Scalar) line=5 col=7
          Sequence line=5 col=22
```

### B.2 `/tmp/yamltest` — `$ go test -run TestMulti -v .`

```
=== RUN   TestMulti
two unknown keys err = "yaml: unmarshal errors:\n  line 4: field nope not found in type main.Sub\n  line 7: field nada not found in type main.Sub"
map[string]Sub, free map key + typo'd inner field err = "yaml: unmarshal errors:\n  line 5: field plot_prioirty not found in type main.Sub"
--- PASS: TestMulti (0.00s)
PASS
ok  	x	0.003s
```

### B.3 `/tmp/yamlanchor` — `$ go run .`

```
=== 1: merge key <<: *d, all keys known ===
err = <nil>

decoded p1 = {Defaults:{Type:scrape With:{PlotPriority:[a] Plot:}} Name:demo Steps:[{Type:organize With:{PlotPriority:[a] Plot:}}]}

=== 2: merge key <<: *d + typo'd key 'tpye' ===
err type = *yaml.TypeError
err string = yaml: unmarshal errors:
  line 8: field tpye not found in type main.Step

=== 3: plain alias (with: *d), no merge key ===
err type = *yaml.TypeError
err string = yaml: unmarshal errors:
  line 2: field type not found in type main.StepWith
  line 3: field with not found in type main.StepWith

decoded p3 = {Defaults:{Type:scrape With:{PlotPriority:[a] Plot:}} Name:demo Steps:[{Type:scrape With:{PlotPriority:[] Plot:}}]}

=== 4: unknown key 'bogus_field' inside the anchored mapping, pulled in via << ===
err type = *yaml.TypeError
err string = yaml: unmarshal errors:
  line 2: field bogus_field not found in type main.Step
  line 2: field bogus_field not found in type main.Step

decoded p4 = {Defaults:{Type: With:{PlotPriority:[] Plot:}} Name:demo Steps:[{Type: With:{PlotPriority:[] Plot:}}]}

=== node tree of docMergeOK ===
 kind=Document tag="" value="" line=1 col=1 alias=0x0
   kind=Mapping tag="!!map" value="" line=1 col=1 alias=0x0
     kind=Scalar tag="!!str" value="defaults" line=1 col=1 alias=0x0
     kind=Mapping tag="!!map" value="" line=1 col=11 alias=0x0
       kind=Scalar tag="!!str" value="type" line=2 col=3 alias=0x0
       kind=Scalar tag="!!str" value="scrape" line=2 col=9 alias=0x0
       kind=Scalar tag="!!str" value="with" line=3 col=3 alias=0x0
       kind=Mapping tag="!!map" value="" line=4 col=5 alias=0x0
         kind=Scalar tag="!!str" value="plot_priority" line=4 col=5 alias=0x0
         kind=Sequence tag="!!seq" value="" line=4 col=20 alias=0x0
           kind=Scalar tag="!!str" value="a" line=4 col=21 alias=0x0
     kind=Scalar tag="!!str" value="name" line=5 col=1 alias=0x0
     kind=Scalar tag="!!str" value="demo" line=5 col=7 alias=0x0
     kind=Scalar tag="!!str" value="steps" line=6 col=1 alias=0x0
     kind=Sequence tag="!!seq" value="" line=7 col=3 alias=0x0
       kind=Mapping tag="!!map" value="" line=7 col=5 alias=0x0
         kind=Scalar tag="!!merge" value="<<" line=7 col=5 alias=0x0
         kind=Alias tag="" value="d" line=7 col=9 alias=0x273fbabf54a0
         kind=Scalar tag="!!str" value="type" line=8 col=5 alias=0x0
         kind=Scalar tag="!!str" value="organize" line=8 col=11 alias=0x0
```

(`alias=0x…` values are process-specific pointers; the pointer printed differs per run.)

### B.4 `/tmp/tomltest` — `$ go run .`

```
########## pelletier/go-toml/v2 ##########
=== pelletier default Unmarshal ===
err = <nil>

=== pelletier DisallowUnknownFields ===
err type = *toml.StrictMissingError
err = strict mode: fields in the document are missing in the target struct

contains "line": false
is *toml.StrictMissingError = true
sme.String() = 4| type = "scrape"
5|
6| [steps.with]
7| plot_prioirty = []
 | ~~~~~~~~~~~~~ unknown field
sme.Error() = strict mode: fields in the document are missing in the target struct
len(sme.Errors) = 1
  Errors[0]: Key=[steps with plot_prioirty] Position=(row=7,col=1) Error()="toml: unknown field" String()="4| type = \"scrape\"\n5|\n6| [steps.with]\n7| plot_prioirty = []\n | ~~~~~~~~~~~~~ unknown field"

=== source doc with line numbers ===
 1 | name = "demo"
 2 | 
 3 | [[steps]]
 4 | type = "scrape"
 5 | 
 6 | [steps.with]
 7 | plot_prioirty = []
 8 | 

########## BurntSushi/toml ##########
=== BurntSushi Decode ===
err = <nil>
md.Undecoded() = [steps.with.plot_prioirty]
md.IsDefined("steps","with","plot_prioirty") = false
md.IsDefined("steps","with","plot_priority") = false

=== BurntSushi strict flag probe (compile-time: does Decoder have a strict method?) ===
bdec = *toml.Decoder
```

### B.5 `/tmp/tomltest` — `$ go test -run TestParseErrors -v ./...`

```
=== RUN   TestParseErrors
BurntSushi syntax err = toml: line 2 (last key "steps"): expected a comma (',') or array terminator (']'), but got end of file
pelletier syntax err type = *toml.DecodeError
err = toml: array is incomplete
pelletier Position() = row 2 col 14
pelletier String() =
1| name = "demo"
2| steps = [1, 2
 |              ~ array is incomplete
--- PASS: TestParseErrors (0.00s)
PASS
ok  	t	0.002s
```

### B.6 `/tmp/gonjatest` — `$ go run .`

```
gonja version: v2.9.1
AutoEscape=true  -> "<title>A &amp; B &lt;C&gt; &#34;quoted&#34;</title>\n<path>/media/A &amp; B &lt;C&gt;/movie.nfo</path>" err=<nil>
AutoEscape=false -> "/media/A & B <C>/movie.nfo" err=<nil>
config.New().AutoEscape = false
gonja.DefaultConfig.AutoEscape = false (package-level global)
autoescape-override in raw env -> "A &amp; B &lt;C&gt; &#34;quoted&#34;|A & B <C> \"quoted\"" err=<nil>
gonja.FromString -> "/media/A & B <C>/movie.nfo" err=<nil>
```

### B.7 `/tmp/gonjatest` — `$ go test -run TestGlobalConfig -v .`

```
=== RUN   TestGlobalConfig
DefaultConfig.AutoEscape=false: "A & B"
after gonja.DefaultConfig.AutoEscape=true: "A &amp; B"
same pointer? DefaultConfig=0xb9cb60
--- PASS: TestGlobalConfig (0.00s)
PASS
ok  	g	0.006s
```

### B.8 `/tmp/yamlnode` — `$ go run .`

```
=== (a) outer strict decode with `With yaml.Node` ===
err = <nil>
step0.With.Kind = 4 (4 == MappingNode), step0.With.Line = 4

=== (b) re-marshal the node, then strict-decode the snippet ===
re-marshalled snippet = "roots: [/mnt/a]\nextensoins: [mkv]\n"
err = yaml: unmarshal errors:
  line 2: field extensoins not found in type main.ScanWith
snippet with only the bad key = "extensoins: [mkv]\n" -> err = yaml: unmarshal errors:
  line 1: field extensoins not found in type main.ScanWith

=== (c) (*yaml.Node).Decode (no re-encode, no way to make it strict) ===
err = <nil>   decoded = {Roots:[/mnt/a] Extensions:[]}

=== (d) baseline: same doc with `with` typed as a struct (correct line) ===
err = yaml: unmarshal errors:
  line 5: field extensoins not found in type main.ScanWith

=== (e) technique 1: walk the already-decoded `With yaml.Node` (positions preserved) ===
  line 5 col 7: field extensoins not found in type main.ScanWith

=== (f) technique 2: decode the whole file into a Node tree, validate by walking ===
  line 5 col 7: field extensoins not found in type main.ScanWith

=== (g) merge key into another step, `with` as a struct ===
err = yaml: unmarshal errors:
  line 6: field bogus_key not found in type main.ScanWith
  line 6: field bogus_key not found in type main.ScanWith
decoded = [{Type:scrape With:{Roots:[/mnt/a] Extensions:[]}} {Type:organize With:{Roots:[/mnt/a] Extensions:[]}}]

=== (h) `<<` itself is never reported as unknown ===
err = <nil>
decoded = [{Type:scrape With:{Roots:[/mnt/a] Extensions:[]}} {Type:organize With:{Roots:[/mnt/a] Extensions:[]}}]
```

### B.9 `/tmp/tomlfollow` — `$ go run .`

```
source doc:
 1 | name = "demo"
 2 | 
 3 | [[steps]]
 4 | type = "scrape"
 5 | 
 6 | [steps.with]
 7 | roots = ["/mnt/a"]
 8 | extensoins = ["mkv"]
 9 | 

=== (a) pelletier strict, `with` as map[string]any (heterogeneous step) ===
err = <nil>

=== (b) pelletier strict, `with` as a struct ===
err = strict mode: fields in the document are missing in the target struct
err type = *toml.StrictMissingError
  Errors[0]: Key=[steps with extensoins] Position=(row=8,col=1) Error()="toml: unknown field"
  String() =
6| [steps.with]
7| roots = ["/mnt/a"]
8| extensoins = ["mkv"]
 | ~~~~~~~~~~ unknown field

=== (c) BurntSushi, same doc ===
err = <nil>
md.Undecoded() = [steps.with.extensoins]
md.Keys() = [name steps steps.type steps.with steps.with.roots steps.with.extensoins]
```

---

## Appendix C: not verified / explicitly out of scope

- No test was written for `fsnotify` itself: the NFS claim rests on the upstream README quote plus
  the old repo's ADR, not on a measurement in this environment (no NFS mount was available to watch).
- No end-to-end test of a real `mdc-lite` config file was done; all tests used synthetic structs and
  documents in `/tmp`.
- The `KnownFields` + `,inline` struct/map interaction (`yaml:",inline"`) was **not** tested; only the
  plain `map[string]any` and `map[string]struct` cases were.
- YAML multi-document streams (`---` separators) were not tested.
- Nothing was verified about `goccy/go-yaml` or other YAML v3 successors; only the maintenance status
  of the upstream `go-yaml/yaml` repository was recorded.
- The hand-rolled validator in Appendix A.5 was written for this test only: it handles structs,
  slices/arrays of structs and pointers via `deref`, but **not** `yaml:",inline"` structs/maps,
  interfaces, or `Unmarshaler` implementations. Its treatment of those cases was not verified.
- `yaml.Node` positions for keys produced by a merge were only observed indirectly (the decode-time
  error points at the anchor); the walker in Appendix A.5 skips `<<` and therefore validates the
  anchored mapping only where it is written, which was not compared against a decode-time error for
  the same file.
- The TOML heterogeneous-step analogue was tested with `map[string]any` only; `pelletier/go-toml/v2`
  has no `Primitive` type equivalent to `BurntSushi`'s, so the "decode the raw value later" pattern
  was not tested for pelletier.
