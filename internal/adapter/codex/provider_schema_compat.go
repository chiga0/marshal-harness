package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

const (
	providerSchemaReceiptVersion = "marshal-operator-codex-provider-schema-preflight/v1"
	providerSchemaProfileVersion = "marshal-operator-codex-provider-schema-profile/v1"
	providerSchemaAuthorityScope = "mac-ordinary-user-operator-local"
	providerSchemaAuthorityClaim = "none"
	providerSchemaCompatible     = "codex-provider-schema-compatible"
	providerSchemaIncompatible   = "codex-provider-schema-incompatible"
	providerSchemaJSONInvalid    = "codex-provider-schema-json-invalid"
	providerProfileInvalid       = "codex-provider-profile-invalid"
	providerSchemaMaxBytes       = 4 << 20
)

var providerAllowedTypes = []string{"array", "boolean", "integer", "null", "number", "object", "string"}
var providerAllowedKeywords = []string{"additionalProperties", "anyOf", "default", "enum", "items", "minimum", "properties", "required", "type"}
var providerUnsupportedKeywords = []string{"$defs", "$id", "$ref", "$schema", "allOf", "const", "format", "maxLength", "minLength", "not", "oneOf", "pattern", "title", "uniqueItems"}

type providerSchemaProfile struct {
	ProfileVersion       string                     `json:"profileVersion"`
	AdapterID            string                     `json:"adapterId"`
	CLICompatibilityLine string                     `json:"cliCompatibilityLine"`
	AuthorityScope       string                     `json:"authorityScope"`
	AuthorityClaim       string                     `json:"authorityClaim"`
	MaxSchemaBytes       int                        `json:"maxSchemaBytes"`
	AllowedTypes         []string                   `json:"allowedTypes"`
	AllowedKeywords      []string                   `json:"allowedKeywords"`
	UnsupportedKeywords  []string                   `json:"unsupportedKeywords"`
	ObjectPolicy         providerSchemaObjectPolicy `json:"objectPolicy"`
	ArrayPolicy          providerSchemaArrayPolicy  `json:"arrayPolicy"`
	Evidence             []providerSchemaEvidence   `json:"evidence"`
}

type providerSchemaObjectPolicy struct {
	AdditionalProperties                 bool `json:"additionalProperties"`
	RequiredMustEqualSortedPropertyNames bool `json:"requiredMustEqualSortedPropertyNames"`
}

type providerSchemaArrayPolicy struct {
	ItemsSchemaRequired bool `json:"itemsSchemaRequired"`
}

type providerSchemaEvidence struct {
	Source      string `json:"source"`
	Observation string `json:"observation"`
}

func frozenProviderSchemaProfileForLine(compatibilityLine string) (providerSchemaProfile, bool) {
	var evidence []providerSchemaEvidence
	switch compatibilityLine {
	case supportedCompatibilityLine:
		evidence = []providerSchemaEvidence{
			{Source: "internal/adapter/codex/execution.go:providerSchemaDocument", Observation: "当前 provider 投影展开 $ref，删除 provider 已拒绝的关键字，把 const 投影为 type+enum，并把全部 object properties 排序后写入 required。"},
			{Source: "mac-codex-ordinary-smoke-r1-r10-20260819", Observation: "真实 Codex CLI 0.145 普通用户链路依次暴露缺 type、not、required、oneOf、format=uri 与 uniqueItems；本 profile 用单次离线遍历聚合这些结构。"},
			{Source: "mac-codex-ordinary-smoke-r16-20260819", Observation: "真实成功投影只使用本 profile 的封闭关键字子集；该证据不构成 Codex 官方 JSON Schema 契约。"},
		}
	case ordinaryUserCompatibilityLine0149:
		evidence = []providerSchemaEvidence{
			{Source: "internal/adapter/codex/execution.go:providerSchemaDocument", Observation: "当前 provider 投影展开 $ref，删除 provider 已拒绝的关键字，把 const 投影为 type+enum，并把全部 object properties 排序后写入 required。"},
			{Source: "codex-cli-0.149.1-mac-ordinary-user-20260825", Observation: "固定 SHA-256 identity 的真实 CLI 接受冻结 argv 与完整 WorkerResult provider schema，并保持 output-last-message 结果传输。"},
			{Source: "codex-cli-0.149.1-stream-json-20260825", Observation: "真实 JSONL 保持 thread.started、turn.started、item.completed(agent_message)、turn.completed；新增 usage 计数不参与授权判断。"},
		}
	default:
		return providerSchemaProfile{}, false
	}
	return providerSchemaProfile{
		ProfileVersion: providerSchemaProfileVersion,
		AdapterID:      adapterID, CLICompatibilityLine: compatibilityLine,
		AuthorityScope: providerSchemaAuthorityScope, AuthorityClaim: providerSchemaAuthorityClaim,
		MaxSchemaBytes:      providerSchemaMaxBytes,
		AllowedTypes:        append([]string(nil), providerAllowedTypes...),
		AllowedKeywords:     append([]string(nil), providerAllowedKeywords...),
		UnsupportedKeywords: append([]string(nil), providerUnsupportedKeywords...),
		ObjectPolicy:        providerSchemaObjectPolicy{AdditionalProperties: false, RequiredMustEqualSortedPropertyNames: true},
		ArrayPolicy:         providerSchemaArrayPolicy{ItemsSchemaRequired: true},
		Evidence:            evidence,
	}, true
}

func frozenProviderSchemaProfileDocumentForLine(compatibilityLine string) ([]byte, error) {
	profile, ok := frozenProviderSchemaProfileForLine(compatibilityLine)
	if !ok {
		return nil, &ProviderSchemaCheckError{ReasonCode: providerProfileInvalid}
	}
	return json.Marshal(profile)
}

// ProviderSchemaIssue is the stable, aggregate compatibility finding emitted
// by the production checker and rendered unchanged by the operator wrapper.
type ProviderSchemaIssue struct {
	Code        string `json:"code"`
	JSONPointer string `json:"jsonPointer"`
	Keyword     string `json:"keyword"`
}

// ProviderSchemaCompatibility is the only rule result for both Adapter Run
// and operator-local preflight receipts. Path/nofollow facts remain with the
// operator wrapper because they describe how its input bytes were obtained.
type ProviderSchemaCompatibility struct {
	ReceiptVersion       string                `json:"receiptVersion"`
	Status               string                `json:"status"`
	ReasonCode           string                `json:"reasonCode"`
	AdapterID            string                `json:"adapterId"`
	CLICompatibilityLine string                `json:"cliCompatibilityLine"`
	AuthorityScope       string                `json:"authorityScope"`
	AuthorityClaim       string                `json:"authorityClaim"`
	ProfileVersion       string                `json:"profileVersion"`
	ProfileDigest        string                `json:"profileDigest"`
	RulesDigest          string                `json:"rulesDigest"`
	SchemaDigest         string                `json:"schemaDigest"`
	SchemaBytes          int                   `json:"schemaBytes"`
	IssueCount           int                   `json:"issueCount"`
	Issues               []ProviderSchemaIssue `json:"issues"`
}

// ProviderSchemaCheckError exposes only a fixed reason code. Raw parser text
// is deliberately not part of the Provider/Worker-visible protocol.
type ProviderSchemaCheckError struct{ ReasonCode string }

func (e *ProviderSchemaCheckError) Error() string { return e.ReasonCode }

func sha256Bytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// decodeStrictJSON rejects duplicate keys, trailing values, non-standard
// constants and numbers that cannot be represented as finite IEEE-754 values.
// It preserves accepted numbers as json.Number so re-encoding is deterministic.
func decodeStrictJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeStrictJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing JSON value")
		}
		return nil, err
	}
	return value, nil
}

func decodeStrictJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			result := make(map[string]any)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				if _, exists := result[key]; exists {
					return nil, fmt.Errorf("duplicate JSON key")
				}
				child, err := decodeStrictJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				result[key] = child
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return nil, fmt.Errorf("unterminated JSON object")
			}
			return result, nil
		case '[':
			result := make([]any, 0)
			for decoder.More() {
				child, err := decodeStrictJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				result = append(result, child)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return nil, fmt.Errorf("unterminated JSON array")
			}
			return result, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter")
		}
	case json.Number:
		number, err := strconv.ParseFloat(string(value), 64)
		if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			return nil, fmt.Errorf("JSON number is not finite")
		}
		return value, nil
	default:
		return value, nil
	}
}

func decodeProviderProfile(raw []byte) (providerSchemaProfile, error) {
	value, err := decodeStrictJSON(raw)
	if err != nil {
		return providerSchemaProfile{}, &ProviderSchemaCheckError{ReasonCode: providerProfileInvalid}
	}
	if _, ok := value.(map[string]any); !ok {
		return providerSchemaProfile{}, &ProviderSchemaCheckError{ReasonCode: providerProfileInvalid}
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return providerSchemaProfile{}, &ProviderSchemaCheckError{ReasonCode: providerProfileInvalid}
	}
	decoder := json.NewDecoder(bytes.NewReader(normalized))
	decoder.DisallowUnknownFields()
	var profile providerSchemaProfile
	if err := decoder.Decode(&profile); err != nil {
		return providerSchemaProfile{}, &ProviderSchemaCheckError{ReasonCode: providerProfileInvalid}
	}
	expected, supported := frozenProviderSchemaProfileForLine(profile.CLICompatibilityLine)
	if !supported || !reflect.DeepEqual(profile, expected) {
		return providerSchemaProfile{}, &ProviderSchemaCheckError{ReasonCode: providerProfileInvalid}
	}
	return profile, nil
}

func providerRulesDigest(profile providerSchemaProfile) (string, error) {
	rules := struct {
		ProfileVersion       string                     `json:"profileVersion"`
		AdapterID            string                     `json:"adapterId"`
		CLICompatibilityLine string                     `json:"cliCompatibilityLine"`
		AuthorityScope       string                     `json:"authorityScope"`
		AuthorityClaim       string                     `json:"authorityClaim"`
		MaxSchemaBytes       int                        `json:"maxSchemaBytes"`
		AllowedTypes         []string                   `json:"allowedTypes"`
		AllowedKeywords      []string                   `json:"allowedKeywords"`
		UnsupportedKeywords  []string                   `json:"unsupportedKeywords"`
		ObjectPolicy         providerSchemaObjectPolicy `json:"objectPolicy"`
		ArrayPolicy          providerSchemaArrayPolicy  `json:"arrayPolicy"`
	}{
		ProfileVersion: profile.ProfileVersion, AdapterID: profile.AdapterID,
		CLICompatibilityLine: profile.CLICompatibilityLine,
		AuthorityScope:       profile.AuthorityScope, AuthorityClaim: profile.AuthorityClaim,
		MaxSchemaBytes: profile.MaxSchemaBytes, AllowedTypes: profile.AllowedTypes,
		AllowedKeywords: profile.AllowedKeywords, UnsupportedKeywords: profile.UnsupportedKeywords,
		ObjectPolicy: profile.ObjectPolicy, ArrayPolicy: profile.ArrayPolicy,
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		return "", err
	}
	return sha256Bytes(raw), nil
}

// CheckProviderSchemaCompatibility is the single production rule authority.
// It aggregates the full issue set deterministically; callers must not launch
// a Provider when it returns a non-pass result or an error.
func CheckProviderSchemaCompatibility(schemaDocument, profileDocument []byte) (ProviderSchemaCompatibility, error) {
	profile, err := decodeProviderProfile(profileDocument)
	if err != nil {
		return ProviderSchemaCompatibility{}, err
	}
	if len(schemaDocument) == 0 || len(schemaDocument) > profile.MaxSchemaBytes {
		return ProviderSchemaCompatibility{}, &ProviderSchemaCheckError{ReasonCode: providerSchemaJSONInvalid}
	}
	value, err := decodeStrictJSON(schemaDocument)
	if err != nil {
		return ProviderSchemaCompatibility{}, &ProviderSchemaCheckError{ReasonCode: providerSchemaJSONInvalid}
	}
	schema, ok := value.(map[string]any)
	if !ok {
		return ProviderSchemaCompatibility{}, &ProviderSchemaCheckError{ReasonCode: providerSchemaJSONInvalid}
	}
	rulesDigest, err := providerRulesDigest(profile)
	if err != nil {
		return ProviderSchemaCompatibility{}, &ProviderSchemaCheckError{ReasonCode: providerProfileInvalid}
	}
	issues := collectProviderSchemaIssues(schema, profile)
	result := ProviderSchemaCompatibility{
		ReceiptVersion: providerSchemaReceiptVersion,
		Status:         "pass", ReasonCode: providerSchemaCompatible,
		AdapterID: profile.AdapterID, CLICompatibilityLine: profile.CLICompatibilityLine,
		AuthorityScope: profile.AuthorityScope, AuthorityClaim: profile.AuthorityClaim,
		ProfileVersion: profile.ProfileVersion, ProfileDigest: sha256Bytes(profileDocument), RulesDigest: rulesDigest,
		SchemaDigest: sha256Bytes(schemaDocument), SchemaBytes: len(schemaDocument),
		Issues: issues, IssueCount: len(issues),
	}
	if len(issues) > 0 {
		result.Status = "fail"
		result.ReasonCode = providerSchemaIncompatible
	}
	return result, nil
}

func collectProviderSchemaIssues(schema map[string]any, profile providerSchemaProfile) []ProviderSchemaIssue {
	allowedTypes := stringSet(profile.AllowedTypes)
	allowedKeywords := stringSet(profile.AllowedKeywords)
	unsupportedKeywords := stringSet(profile.UnsupportedKeywords)
	issues := make([]ProviderSchemaIssue, 0)
	seen := make(map[string]bool)
	add := func(code, pointer, keyword string) {
		key := pointer + "\x00" + code + "\x00" + keyword
		if !seen[key] {
			seen[key] = true
			issues = append(issues, ProviderSchemaIssue{Code: code, JSONPointer: pointer, Keyword: keyword})
		}
	}
	var walk func(any, string)
	walk = func(value any, pointer string) {
		node, ok := value.(map[string]any)
		if !ok {
			add("keyword-value-invalid", pointer, "schema")
			return
		}
		typeValue, hasType := node["type"]
		schemaType, typeOK := typeValue.(string)
		if !hasType {
			add("missing-type", pointer, "type")
		} else if !typeOK || !allowedTypes[schemaType] {
			add("type-invalid", pointerChild(pointer, "type"), "type")
		}

		keys := make([]string, 0, len(node))
		for keyword := range node {
			keys = append(keys, keyword)
		}
		sort.Strings(keys)
		for _, keyword := range keys {
			if unsupportedKeywords[keyword] {
				add("unsupported-keyword", pointerChild(pointer, keyword), keyword)
			} else if !allowedKeywords[keyword] {
				add("unknown-keyword", pointerChild(pointer, keyword), keyword)
			}
		}

		properties, hasProperties := node["properties"]
		propertyMap, propertiesOK := properties.(map[string]any)
		if hasProperties && !propertiesOK {
			add("object-properties-invalid", pointerChild(pointer, "properties"), "properties")
		}
		if propertiesOK {
			names := make([]string, 0, len(propertyMap))
			for name := range propertyMap {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				walk(propertyMap[name], pointerChild(pointerChild(pointer, "properties"), name))
			}
		}

		required, hasRequired := node["required"]
		requiredValues, requiredOK := uniqueStringArray(required)
		if hasRequired && !requiredOK {
			add("keyword-value-invalid", pointerChild(pointer, "required"), "required")
		}
		additional, hasAdditional := node["additionalProperties"]
		additionalBool, additionalOK := additional.(bool)
		if hasAdditional && !additionalOK {
			add("keyword-value-invalid", pointerChild(pointer, "additionalProperties"), "additionalProperties")
		}
		if typeOK && schemaType == "object" {
			if !propertiesOK {
				add("object-properties-invalid", pointerChild(pointer, "properties"), "properties")
			}
			if !hasAdditional || (additionalOK && additionalBool) {
				add("additional-properties-not-false", pointerChild(pointer, "additionalProperties"), "additionalProperties")
			}
			if requiredOK {
				expected := make([]string, 0, len(propertyMap))
				for name := range propertyMap {
					expected = append(expected, name)
				}
				sort.Strings(expected)
				if !reflect.DeepEqual(requiredValues, expected) {
					add("required-properties-mismatch", pointerChild(pointer, "required"), "required")
				}
			} else if !hasRequired {
				add("required-properties-mismatch", pointerChild(pointer, "required"), "required")
			}
		}

		items, hasItems := node["items"]
		_, itemsOK := items.(map[string]any)
		if hasItems {
			if !itemsOK {
				add("keyword-value-invalid", pointerChild(pointer, "items"), "items")
			} else {
				walk(items, pointerChild(pointer, "items"))
			}
		} else if typeOK && schemaType == "array" {
			add("array-items-missing", pointerChild(pointer, "items"), "items")
		}

		if alternatives, exists := node["anyOf"]; exists {
			list, ok := alternatives.([]any)
			if !ok || len(list) == 0 {
				add("anyof-shape-invalid", pointerChild(pointer, "anyOf"), "anyOf")
			} else {
				for index, alternative := range list {
					if _, ok := alternative.(map[string]any); !ok {
						add("anyof-shape-invalid", pointerChild(pointerChild(pointer, "anyOf"), index), "anyOf")
					}
					walk(alternative, pointerChild(pointerChild(pointer, "anyOf"), index))
				}
			}
		}

		if enumValue, exists := node["enum"]; exists {
			list, ok := enumValue.([]any)
			valid := ok && len(list) > 0
			encoded := make(map[string]bool)
			if valid {
				for _, item := range list {
					key, keyOK := canonicalValueKey(item)
					if !keyOK || encoded[key] || (typeOK && allowedTypes[schemaType] && !valueMatchesProviderType(item, schemaType)) {
						valid = false
						break
					}
					encoded[key] = true
				}
			}
			if !valid {
				add("enum-shape-invalid", pointerChild(pointer, "enum"), "enum")
			}
		}

		if minimum, exists := node["minimum"]; exists {
			if !finiteJSONNumber(minimum) || !typeOK || (schemaType != "integer" && schemaType != "number") {
				add("keyword-value-invalid", pointerChild(pointer, "minimum"), "minimum")
			}
		}
		if defaultValue, exists := node["default"]; exists {
			if !typeOK || !allowedTypes[schemaType] || !valueMatchesProviderType(defaultValue, schemaType) {
				add("keyword-value-invalid", pointerChild(pointer, "default"), "default")
			}
		}

		if nested, ok := node["not"].(map[string]any); ok {
			walk(nested, pointerChild(pointer, "not"))
		}
		if definitions, ok := node["$defs"].(map[string]any); ok {
			names := make([]string, 0, len(definitions))
			for name := range definitions {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				walk(definitions[name], pointerChild(pointerChild(pointer, "$defs"), name))
			}
		}
		for _, keyword := range []string{"allOf", "oneOf"} {
			if list, ok := node[keyword].([]any); ok {
				for index, child := range list {
					walk(child, pointerChild(pointerChild(pointer, keyword), index))
				}
			}
		}
	}
	walk(schema, "")
	sort.Slice(issues, func(i, j int) bool {
		left, right := issues[i], issues[j]
		if left.JSONPointer != right.JSONPointer {
			return left.JSONPointer < right.JSONPointer
		}
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		return left.Keyword < right.Keyword
	})
	return issues
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func pointerChild(pointer string, token any) string {
	escaped := strings.ReplaceAll(strings.ReplaceAll(fmt.Sprint(token), "~", "~0"), "/", "~1")
	return pointer + "/" + escaped
}

func uniqueStringArray(value any) ([]string, bool) {
	list, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(list))
	seen := make(map[string]bool)
	for _, item := range list {
		text, ok := item.(string)
		if !ok || seen[text] {
			return nil, false
		}
		seen[text] = true
		result = append(result, text)
	}
	return result, true
}

func finiteJSONNumber(value any) bool {
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	parsed, err := strconv.ParseFloat(string(number), 64)
	return err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed)
}

func valueMatchesProviderType(value any, schemaType string) bool {
	switch schemaType {
	case "null":
		return value == nil
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		parsed, err := strconv.ParseFloat(string(number), 64)
		return err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed) && math.Trunc(parsed) == parsed
	case "number":
		return finiteJSONNumber(value)
	case "string":
		_, ok := value.(string)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	default:
		return false
	}
}

func canonicalValueKey(value any) (string, bool) {
	var result strings.Builder
	if !appendCanonicalValue(&result, value) {
		return "", false
	}
	return result.String(), true
}

func appendCanonicalValue(result *strings.Builder, value any) bool {
	switch typed := value.(type) {
	case nil:
		result.WriteString("n;")
	case bool:
		if typed {
			result.WriteString("b1;")
		} else {
			result.WriteString("b0;")
		}
	case json.Number:
		var number big.Rat
		if _, ok := number.SetString(string(typed)); !ok {
			return false
		}
		appendCanonicalPart(result, 'd', number.RatString())
	case string:
		appendCanonicalPart(result, 's', typed)
	case []any:
		result.WriteByte('a')
		result.WriteString(strconv.Itoa(len(typed)))
		result.WriteByte(':')
		for _, item := range typed {
			var child strings.Builder
			if !appendCanonicalValue(&child, item) {
				return false
			}
			appendCanonicalPart(result, 'v', child.String())
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		result.WriteByte('o')
		result.WriteString(strconv.Itoa(len(keys)))
		result.WriteByte(':')
		for _, key := range keys {
			appendCanonicalPart(result, 'k', key)
			var child strings.Builder
			if !appendCanonicalValue(&child, typed[key]) {
				return false
			}
			appendCanonicalPart(result, 'v', child.String())
		}
	default:
		return false
	}
	return true
}

func appendCanonicalPart(result *strings.Builder, marker byte, value string) {
	result.WriteByte(marker)
	result.WriteString(strconv.Itoa(len(value)))
	result.WriteByte(':')
	result.WriteString(value)
}
