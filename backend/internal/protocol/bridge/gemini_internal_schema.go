package bridge

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Internal Gemini function schemas use a restricted shape, so richer JSON
// Schema input is approximated before becoming a declaration. Root definitions
// expand through their final reference component, allOf contributes fields,
// and unions or tuple items select one ranked representative instead of
// retaining every possible validation branch.
//
// Cleaning mutates supplied maps and can retain shared values from allOf
// branches. Unsupported constraints become description hints before the field
// whitelist removes them; exact lower-case empty objects gain a required
// reason string. Type normalization and enum conversion then adapt the
// selected shape. Traversal gives properties priority over items and other
// containers, and branch fields introduced later are not revisited, so callers
// cannot treat this conversion as equivalent schema validation or a universal
// recursive normalizer.

// CleanJSONSchema adapts a caller-owned schema map for internal Gemini
// function declarations and returns the mutated map, or nil for nil input.
// Root definitions are removed and used for expansion before recursive
// approximation.
//
// Reference expansion, allOf merging, branch selection, and constraint hints
// deliberately narrow or loosen the represented validation rules. Input needs
// acyclic maps/arrays and terminating reference chains because these routines
// have no cycle guard. Properties-first traversal and later branch insertion
// can leave some nested constructs unvisited. Union required arrays need
// comparable values for membership checks; malformed repeated map/slice values
// can panic before the later string-name filter. Existing aliases observe
// in-place changes, and merged values can remain shared with source branches.
// Adapted from Antigravity-Manager/src-tauri/src/proxy/common/json_schema.rs.
func CleanJSONSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	// Collect and remove root definitions before expansion, so nested references
	// can use that lookup even after the declaration's definition fields
	// disappear.
	flattenRefs(schema, extractDefs(schema))

	cleaned := cleanJSONSchemaRecursive(schema)
	result, ok := cleaned.(map[string]any)
	if !ok {
		return nil
	}

	return result
}

// extractDefs removes map-valued root $defs and definitions entries and
// returns their combined name lookup. Legacy definitions overwrite duplicate
// $defs names; definition values are shared with their source maps, and
// differently typed entries remain untouched here.
func extractDefs(schema map[string]any) map[string]any {
	defs := make(map[string]any)
	if d, ok := schema["$defs"].(map[string]any); ok {
		maps.Copy(defs, d)
		delete(schema, "$defs")
	}
	if d, ok := schema["definitions"].(map[string]any); ok {
		maps.Copy(defs, d)
		delete(schema, "definitions")
	}
	return defs
}

// flattenRefs expands references in a caller-owned map using the supplied root
// definition lookup. It resolves the final slash component, removes a string
// $ref when definitions exist, and fills keys absent from the referencing map.
//
// Copied ordinary JSON maps/arrays isolate expanded values before later
// cleanup, while other dynamic values remain shared through deepCopy.
// Expansion continues through nested maps and map members of arrays. Missing
// definitions leave the removed reference unrepresented, and an empty lookup
// skips traversal. Callers supply terminating reference chains and acyclic
// containers because recursive references are not tracked.
func flattenRefs(schema map[string]any, defs map[string]any) {
	if len(defs) == 0 {
		return
	}

	if ref, ok := schema["$ref"].(string); ok {
		delete(schema, "$ref")
		// Use the final slash component as a root definition name; prefixes and JSON
		// Pointer escaping are not interpreted by this internal lookup.
		parts := strings.Split(ref, "/")
		refName := parts[len(parts)-1]

		if defSchema, exists := defs[refName]; exists {
			if defMap, ok := defSchema.(map[string]any); ok {
				// Keep keys already supplied at the reference site, and copy recognized
				// nested JSON containers from missing definition fields before recursive
				// cleanup mutates them.
				for k, v := range defMap {
					if _, has := schema[k]; !has {
						schema[k] = deepCopy(v)
					}
				}
				flattenRefs(schema, defs)
			}
		}
	}

	for _, v := range schema {
		if subMap, ok := v.(map[string]any); ok {
			flattenRefs(subMap, defs)
		} else if subArr, ok := v.([]any); ok {
			for _, item := range subArr {
				if itemMap, ok := item.(map[string]any); ok {
					flattenRefs(itemMap, defs)
				}
			}
		}
	}
}

// deepCopy recursively copies ordinary map[string]any and []any containers for
// reference expansion and branch insertion. Other dynamic values are returned
// as supplied, including shared pointers or named containers; callers supply
// acyclic JSON-shaped input.
func deepCopy(src any) any {
	if src == nil {
		return nil
	}
	switch v := src.(type) {
	case map[string]any:
		dst := make(map[string]any)
		for k, val := range v {
			dst[k] = deepCopy(val)
		}
		return dst
	case []any:
		dst := make([]any, len(v))
		for i, val := range v {
			dst[i] = deepCopy(val)
		}
		return dst
	default:
		return src
	}
}

// cleanJSONSchemaRecursive approximates a schema map in place and returns
// other values unchanged. It merges allOf first, recursively visits properties
// when present, otherwise items, otherwise generic nested containers, then
// selects an eligible union branch.
//
// Schema-like keys activate constraint hints and a field whitelist. Exact
// lower-case empty object types gain a required reason parameter before type
// normalization; required names then retain only string keys present in
// properties. Recognized type strings/arrays choose a lower-case non-null
// representation and append a nullable hint. Nonstring enum values become
// formatted strings and switch the type to string. Fields copied from a
// selected union after traversal are not recursively cleaned again.
func cleanJSONSchemaRecursive(value any) any {
	schemaMap, ok := value.(map[string]any)
	if !ok {
		return value
	}

	// Merge immediate allOf fields before traversing properties, so newly
	// contributed properties and required names participate in the following
	// cleanup phases.
	mergeAllOf(schemaMap)

	// A properties map selects property traversal even when empty; items and
	// generic sibling containers are visited only when that properties shape is
	// absent.
	if props, ok := schemaMap["properties"].(map[string]any); ok {
		for _, v := range props {
			cleanJSONSchemaRecursive(v)
		}
	} else if items, ok := schemaMap["items"]; ok {
		// Internal item conversion replaces tuple arrays with one representative
		// schema, so positional tuple constraints do not survive as separate element
		// validators.
		if itemsArr, ok := items.([]any); ok {
			// Use the highest-ranked member as the shared item shape; first-rank ties
			// retain input order and may select a malformed or nil value.
			best := extractBestSchemaFromUnion(itemsArr)
			if best == nil {
				// A nil selected member needs a concrete item object for this converter;
				// substitute string when an empty or zero-ranked selection provides none.
				best = map[string]any{"type": "string"}
			}
			cleanedBest := cleanJSONSchemaRecursive(best)
			schemaMap["items"] = cleanedBest
		} else {
			cleanJSONSchemaRecursive(items)
		}
	} else {
		for _, v := range schemaMap {
			if _, isMap := v.(map[string]any); isMap {
				cleanJSONSchemaRecursive(v)
			} else if arr, isArr := v.([]any); isArr {
				for _, item := range arr {
					cleanJSONSchemaRecursive(item)
				}
			}
		}
	}

	// Missing, nonstring, or empty type and exact object permit anyOf, then oneOf,
	// representative selection; other type strings keep their own fields until
	// the whitelist removes union keys.
	var unionArray []any
	typeStr, _ := schemaMap["type"].(string)
	if typeStr == "" || typeStr == "object" {
		if anyOf, ok := schemaMap["anyOf"].([]any); ok {
			unionArray = anyOf
		} else if oneOf, ok := schemaMap["oneOf"].([]any); ok {
			unionArray = oneOf
		}
	}

	// The selected map contributes missing fields and properties while required
	// names are combined; parent fields win, and introduced branch fields receive
	// no second recursive pass.
	if len(unionArray) > 0 {
		if bestBranch := extractBestSchemaFromUnion(unionArray); bestBranch != nil {
			if bestMap, ok := bestBranch.(map[string]any); ok {
				for k, v := range bestMap {
					if k == "properties" {
						targetProps, _ := schemaMap["properties"].(map[string]any)
						if targetProps == nil {
							targetProps = make(map[string]any)
							schemaMap["properties"] = targetProps
						}
						if sourceProps, ok := v.(map[string]any); ok {
							for pk, pv := range sourceProps {
								if _, exists := targetProps[pk]; !exists {
									targetProps[pk] = deepCopy(pv)
								}
							}
						}
					} else if k == "required" {
						targetReq, _ := schemaMap["required"].([]any)
						if sourceReq, ok := v.([]any); ok {
							// Membership compares interface values before the later required-name
							// filter; valid string entries avoid equality panics from repeated compound values.
							for _, rv := range sourceReq {
								exists := slices.Contains(targetReq, rv)
								if !exists {
									targetReq = append(targetReq, rv)
								}
							}
							schemaMap["required"] = targetReq
						}
					} else if _, exists := schemaMap[k]; !exists {
						schemaMap[k] = deepCopy(v)
					}
				}
			}
		}
	}

	// Apply the whitelist only after merging/selection leaves schema-like keys;
	// generic container maps keep unrelated entries so their property names are
	// not uniformly filtered.
	looksLikeSchema := hasKey(schemaMap, "type") ||
		hasKey(schemaMap, "properties") ||
		hasKey(schemaMap, "items") ||
		hasKey(schemaMap, "enum") ||
		hasKey(schemaMap, "anyOf") ||
		hasKey(schemaMap, "oneOf") ||
		hasKey(schemaMap, "allOf")

	if looksLikeSchema {
		// Record unsupported constraint values as model-readable description hints
		// before removing their validation fields; the resulting declaration cannot
		// enforce those original restrictions.
		migrateConstraints(schemaMap)

		allowedFields := map[string]bool{
			"type":        true,
			"description": true,
			"properties":  true,
			"required":    true,
			"items":       true,
			"enum":        true,
			"title":       true,
		}
		for k := range schemaMap {
			if !allowedFields[k] {
				delete(schemaMap, k)
			}
		}

		// An exact lower-case object with no nonempty property map gains a required
		// reason string, giving this internal tool declaration one named argument.
		if t, _ := schemaMap["type"].(string); t == "object" {
			hasProps := false
			if props, ok := schemaMap["properties"].(map[string]any); ok && len(props) > 0 {
				hasProps = true
			}
			if !hasProps {
				schemaMap["properties"] = map[string]any{
					"reason": map[string]any{
						"type":        "string",
						"description": "Reason for calling this tool",
					},
				}
				schemaMap["required"] = []any{"reason"}
			}
		}

		// Keep required entries only when they are strings naming an existing
		// property, so removed or malformed names cannot demand an undeclared
		// argument.
		if props, ok := schemaMap["properties"].(map[string]any); ok {
			if req, ok := schemaMap["required"].([]any); ok {
				var validReq []any
				for _, r := range req {
					if rStr, ok := r.(string); ok {
						if _, exists := props[rStr]; exists {
							validReq = append(validReq, r)
						}
					}
				}
				if len(validReq) > 0 {
					schemaMap["required"] = validReq
				} else {
					delete(schemaMap, "required")
				}
			}
		}

		// Choose one lower-case type and represent null alternatives as description
		// text; type conversion happens after empty-object repair, so later object
		// defaults do not trigger it.
		isEffectivelyNullable := false
		if typeVal, exists := schemaMap["type"]; exists {
			var selectedType string
			switch v := typeVal.(type) {
			case string:
				lower := strings.ToLower(v)
				if lower == "null" {
					isEffectivelyNullable = true
					selectedType = "string"
				} else {
					selectedType = lower
				}
			case []any:
				// A type array chooses its first non-null string while remembering every
				// null member; an array containing no usable non-null string becomes
				// string.
				for _, t := range v {
					if ts, ok := t.(string); ok {
						lower := strings.ToLower(ts)
						if lower == "null" {
							isEffectivelyNullable = true
						} else if selectedType == "" {
							selectedType = lower
						}
					}
				}
				if selectedType == "" {
					selectedType = "string"
				}
			}
			schemaMap["type"] = selectedType
		} else {
			// Schema-like maps lacking type receive object here, after empty-object
			// argument repair; that late default can therefore leave an object without
			// a reason property.
			schemaMap["type"] = "object"
		}

		// Carry lost null alternatives into a textual nullable hint, reusing
		// descriptions that already contain the exact lower-case substring to avoid
		// adding it again.
		if isEffectivelyNullable {
			desc, _ := schemaMap["description"].(string)
			if !strings.Contains(desc, "nullable") {
				if desc != "" {
					desc += " "
				}
				desc += "(nullable)"
				schemaMap["description"] = desc
			}
		}

		// Convert every nonstring enum member with display formatting, using literal
		// null for nil; compound members are formatted here rather than rejected as
		// in native cleanup.
		if enumVals, ok := schemaMap["enum"].([]any); ok {
			hasNonString := false
			for i, val := range enumVals {
				if _, isStr := val.(string); !isStr {
					hasNonString = true
					if val == nil {
						enumVals[i] = "null"
					} else {
						enumVals[i] = fmt.Sprintf("%v", val)
					}
				}
			}
			// Any converted enum member changes the schema type to string because the
			// declaration now carries formatted text values, even when its former type
			// was numeric.
			if hasNonString {
				schemaMap["type"] = "string"
			}
		}
	}

	return schemaMap
}

// hasKey reports whether a map contains the supplied key, including a key with
// a nil value.
func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

// migrateConstraints appends present nonnil constraint values to the schema
// description in a fixed key order. The caller subsequently removes
// unsupported validation fields, so these hints retain readable restrictions
// for the model rather than machine-enforced validation.
//
// The exact generated suffix is added once, while existing string description
// text remains ahead of it. Nonstring descriptions contribute no existing text
// through the string assertion. Values use display formatting and can describe
// richer structures, but this conversion does not evaluate those constraints
// or reconstruct equivalent schema fields.
func migrateConstraints(m map[string]any) {
	constraints := []struct {
		// key selects the validator value; label names that value in the generated
		// suffix. The list order determines the hint order within description text.
		key   string // source schema constraint key
		label string // generated description hint label
	}{
		{"minLength", "minLen"},
		{"maxLength", "maxLen"},
		{"pattern", "pattern"},
		{"minimum", "min"},
		{"maximum", "max"},
		{"multipleOf", "multipleOf"},
		{"exclusiveMinimum", "exclMin"},
		{"exclusiveMaximum", "exclMax"},
		{"minItems", "minItems"},
		{"maxItems", "maxItems"},
		{"propertyNames", "propertyNames"},
		{"format", "format"},
	}

	var hints []string
	for _, c := range constraints {
		if val, ok := m[c.key]; ok && val != nil {
			hints = append(hints, fmt.Sprintf("%s: %v", c.label, val))
		}
	}

	// Append the complete ordered hint suffix only when that exact text is
	// absent, letting repeated cleanup retain one copy of an unchanged constraint
	// set.
	if len(hints) > 0 {
		suffix := fmt.Sprintf(" [Constraint: %s]", strings.Join(hints, ", "))
		desc, _ := m["description"].(string)
		if !strings.Contains(desc, suffix) {
			m["description"] = desc + suffix
		}
	}
}

// mergeAllOf removes an array-valued allOf and combines its immediate map
// branches into the parent. Existing parent fields and properties win; later
// branches win property conflicts, while the first branch supplies other
// missing fields.
//
// Required names are combined as strings with existing string entries kept in
// order and new names appended in map iteration order. Merged values remain
// shared with branch maps, and nested allOf keys inside these immediate
// branches are skipped. This approximates intersecting schemas; later cleanup
// filters required names against resulting properties.
func mergeAllOf(m map[string]any) {
	allOf, ok := m["allOf"].([]any)
	if !ok {
		return
	}
	delete(m, "allOf")

	mergedProps := make(map[string]any)
	mergedReq := make(map[string]bool)
	otherFields := make(map[string]any)

	// Later immediate branches overwrite earlier property values, while
	// first-seen nonproperty fields are retained; the parent's existing values
	// take precedence when these collections are applied.
	for _, sub := range allOf {
		if subMap, ok := sub.(map[string]any); ok {
			if props, ok := subMap["properties"].(map[string]any); ok {
				maps.Copy(mergedProps, props)
			}
			if reqs, ok := subMap["required"].([]any); ok {
				for _, r := range reqs {
					if s, ok := r.(string); ok {
						mergedReq[s] = true
					}
				}
			}
			for k, v := range subMap {
				if k != "properties" && k != "required" && k != "allOf" {
					if _, exists := otherFields[k]; !exists {
						otherFields[k] = v
					}
				}
			}
		}
	}

	for k, v := range otherFields {
		if _, exists := m[k]; !exists {
			m[k] = v
		}
	}
	if len(mergedProps) > 0 {
		existProps, _ := m["properties"].(map[string]any)
		if existProps == nil {
			existProps = make(map[string]any)
			m["properties"] = existProps
		}
		for k, v := range mergedProps {
			if _, exists := existProps[k]; !exists {
				existProps[k] = v
			}
		}
	}
	// Keep existing string required names in order and append remaining branch
	// names once; new-name order follows map iteration, then recursive cleanup
	// validates membership.
	if len(mergedReq) > 0 {
		existReq, _ := m["required"].([]any)
		var validReqs []any
		for _, r := range existReq {
			if s, ok := r.(string); ok {
				validReqs = append(validReqs, s)
				delete(mergedReq, s)
			}
		}
		for r := range mergedReq {
			validReqs = append(validReqs, r)
		}
		m["required"] = validReqs
	}
}

// extractBestSchemaFromUnion returns the first supplied value with the highest
// scoreSchemaOption rank. Empty input returns nil, and the selected value
// remains shared with the supplied union array.
//
// Structural object hints outrank array hints, then other exact non-null type
// strings; malformed and untyped values can still win a zero-score tie. A nil
// first member can therefore remain the result when every member scores zero.
// Tuple conversion substitutes a string schema for a nil result, while union
// conversion copies fields only from a selected map.
func extractBestSchemaFromUnion(unionArray []any) any {
	var bestOption any
	bestScore := -1

	// Replace the current selection only for a strictly higher score, so
	// equal-ranked candidates retain input order and nil can win an all-zero tie.
	for _, item := range unionArray {
		score := scoreSchemaOption(item)
		if score > bestScore {
			bestScore = score
			bestOption = item
		}
	}
	return bestOption
}

// scoreSchemaOption ranks a map for internal tuple and union approximation,
// using object hints before arrays and other non-null type labels. Non-map
// values score zero, and structural key presence counts even when its value
// is nil.
//
// properties or exact object yields rank three; items or exact array yields
// two. Remaining nonempty types other than exact lower-case null yield one,
// so case and unsupported spellings can affect selection.
// extractBestSchemaFromUnion keeps the first equal-ranked value, and the
// selected shape can discard alternatives whose validation differs despite
// their lower score.
func scoreSchemaOption(val any) int {
	m, ok := val.(map[string]any)
	if !ok {
		return 0
	}
	typeStr, _ := m["type"].(string)

	// Structural hints outrank scalar labels during branch approximation.
	if hasKey(m, "properties") || typeStr == "object" {
		return 3
	}
	if hasKey(m, "items") || typeStr == "array" {
		return 2
	}
	if typeStr != "" && typeStr != "null" {
		return 1
	}
	return 0
}

// DeepCleanUndefined removes map entries whose value is the exact string
// [undefined] while visiting ordinary nested maps and arrays in place. Array
// scalar values retain their positions, other dynamic containers are
// unchanged, and callers supply acyclic shared input.
func DeepCleanUndefined(value any) {
	if value == nil {
		return
	}
	switch v := value.(type) {
	case map[string]any:
		for k, val := range v {
			// Omit map entries carrying this sentinel from the shared schema; scalar
			// array sentinels keep their positions because only map entries are
			// removed.
			if s, ok := val.(string); ok && s == "[undefined]" {
				delete(v, k)
				continue
			}
			DeepCleanUndefined(val)
		}
	case []any:
		for _, val := range v {
			DeepCleanUndefined(val)
		}
	}
}
