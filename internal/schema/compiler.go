package schema

import (
	"fmt"
	"strings"

	"comini/internal/domain"
)

// SanitizeFunctionName ensures the function name conforms to Gemini API constraints:
// - Must start with a letter or an underscore.
// - Must be alphanumeric (a-z, A-Z, 0-9), underscores (_), dots (.), colons (:), or dashes (-).
// - Maximum length of 128 characters.
func SanitizeFunctionName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "_tool"
	}

	var sb strings.Builder
	for i, r := range name {
		isValidChar := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '.' || r == ':' || r == '-'

		if i == 0 {
			isFirstValid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
			if !isFirstValid {
				sb.WriteRune('_')
				if r >= '0' && r <= '9' {
					sb.WriteRune(r)
				}
				continue
			}
		}

		if isValidChar {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('_')
		}
	}

	res := sb.String()
	if len(res) > 128 {
		res = res[:128]
	}
	return res
}

// ExtractAndCompileTools extracts tools from tool definitions (including namespaces and custom tools)
// and compiles them to Gemini FunctionDeclarations along with a metadata lookup map.
func ExtractAndCompileTools(toolDefs []domain.ToolDefinition) ([]map[string]any, map[string]domain.ToolMeta) {
	var decls []map[string]any
	metaMap := make(map[string]domain.ToolMeta)

	var processTool func(t domain.ToolDefinition, ns string)
	processTool = func(t domain.ToolDefinition, ns string) {
		switch t.Type {
		case "namespace":
			namespaceName := t.Name
			if ns != "" {
				namespaceName = ns + ":" + t.Name
			}
			for _, inner := range t.Tools {
				processTool(inner, namespaceName)
			}

		case "custom":
			// Custom tool (like apply_patch) expects raw input
			origName := t.Name
			if origName == "" {
				origName = "custom_tool"
			}
			wireName := origName
			if ns != "" {
				wireName = fmt.Sprintf("%s:%s", ns, origName)
			}
			sanitized := SanitizeFunctionName(wireName)
			meta := domain.ToolMeta{
				OriginalName: origName,
				Namespace:    ns,
				IsCustom:     true,
			}
			metaMap[sanitized] = meta
			metaMap[wireName] = meta
			metaMap[origName] = meta
			params := map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"input": map[string]any{
						"type":        "STRING",
						"description": "Raw tool input string (for apply_patch, the complete patch format).",
					},
				},
				"required": []string{"input"},
			}
			desc := t.Description
			if desc == "" {
				desc = "Custom freeform tool"
			}
			decls = append(decls, map[string]any{
				"name":        sanitized,
				"description": desc,
				"parameters":  params,
			})

		case "tool_search":
			origName := "tool_search"
			sanitized := "tool_search"
			meta := domain.ToolMeta{
				OriginalName: origName,
				Namespace:    ns,
				IsToolSearch: true,
			}
			metaMap[sanitized] = meta
			metaMap[origName] = meta
			params := map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "STRING",
						"description": "Search query for tools to load.",
					},
					"limit": map[string]any{
						"type":        "INTEGER",
						"description": "Maximum number of tools to return.",
					},
				},
				"required": []string{"query"},
			}
			decls = append(decls, map[string]any{
				"name":        sanitized,
				"description": "Search for additional tools to load for next turn.",
				"parameters":  params,
			})

		case "web_search", "image_generation":
			// Hosted server-side tools are ignored unless provided as regular functions
			return

		default:
			// Regular function
			origName := t.Name
			if origName == "" {
				return
			}
			wireName := origName
			if ns != "" {
				wireName = fmt.Sprintf("%s:%s", ns, origName)
			}
			sanitized := SanitizeFunctionName(wireName)
			meta := domain.ToolMeta{
				OriginalName: origName,
				Namespace:    ns,
			}
			metaMap[sanitized] = meta
			metaMap[wireName] = meta
			metaMap[origName] = meta
			decls = append(decls, CompileGeminiFunctionDeclaration(t, sanitized))
		}
	}

	for _, toolDef := range toolDefs {
		processTool(toolDef, "")
	}

	return decls, metaMap
}

// CompileGeminiFunctionDeclaration translates OpenAPI / JSON Schema to Gemini FunctionDeclaration.
// Strips keywords unsupported by Gemini: $ref, pattern, oneOf, anyOf, additionalProperties.
func CompileGeminiFunctionDeclaration(tool domain.ToolDefinition, sanitizedName string) map[string]any {
	params := cleanSchemaNode(tool.Parameters)
	if params == nil {
		params = map[string]any{
			"type":       "OBJECT",
			"properties": map[string]any{},
		}
	}

	return map[string]any{
		"name":        sanitizedName,
		"description": tool.Description,
		"parameters":  params,
	}
}

func cleanSchemaNode(node any) map[string]any {
	raw, ok := node.(map[string]any)
	if !ok || raw == nil {
		return nil
	}

	out := make(map[string]any)

	// Map type to uppercase (OBJECT, STRING, INTEGER, NUMBER, BOOLEAN, ARRAY)
	if t, ok := raw["type"].(string); ok {
		out["type"] = strings.ToUpper(t)
	} else if types, ok := raw["type"].([]any); ok {
		for _, item := range types {
			if s, ok := item.(string); ok {
				if strings.ToLower(s) == "null" {
					out["nullable"] = true
				} else if _, hasType := out["type"]; !hasType {
					out["type"] = strings.ToUpper(s)
				}
			}
		}
	} else if _, hasProps := raw["properties"]; hasProps {
		out["type"] = "OBJECT"
	}

	if nullable, ok := raw["nullable"].(bool); ok && nullable {
		out["nullable"] = true
	}

	if desc, ok := raw["description"].(string); ok {
		out["description"] = desc
	}

	if enums, ok := raw["enum"].([]any); ok {
		strEnums := make([]string, 0, len(enums))
		for _, e := range enums {
			if s, ok := e.(string); ok {
				strEnums = append(strEnums, s)
			}
		}
		if len(strEnums) > 0 {
			out["enum"] = strEnums
		}
	}

	if reqs, ok := raw["required"].([]any); ok {
		strReqs := make([]string, 0, len(reqs))
		for _, r := range reqs {
			if s, ok := r.(string); ok {
				strReqs = append(strReqs, s)
			}
		}
		if len(strReqs) > 0 {
			out["required"] = strReqs
		}
	}

	if props, ok := raw["properties"].(map[string]any); ok {
		cleanedProps := make(map[string]any)
		for k, v := range props {
			if cv := cleanSchemaNode(v); cv != nil {
				cleanedProps[k] = cv
			}
		}
		out["properties"] = cleanedProps
	}

	if items, ok := raw["items"]; ok {
		if ci := cleanSchemaNode(items); ci != nil {
			out["items"] = ci
		}
	}

	if allOf, ok := raw["allOf"].([]any); ok && len(allOf) > 0 {
		for _, item := range allOf {
			if itemMap, ok := item.(map[string]any); ok {
				if itemProps, ok := itemMap["properties"].(map[string]any); ok {
					if out["properties"] == nil {
						out["properties"] = make(map[string]any)
					}
					props := out["properties"].(map[string]any)
					for pk, pv := range itemProps {
						if cp := cleanSchemaNode(pv); cp != nil {
							props[pk] = cp
						}
					}
				}
			}
		}
		if _, hasType := out["type"]; !hasType {
			out["type"] = "OBJECT"
		}
	}

	// Handle anyOf / oneOf fallback
	if _, hasType := out["type"]; !hasType {
		if anyOf, ok := raw["anyOf"].([]any); ok && len(anyOf) > 0 {
			return cleanSchemaNode(anyOf[0])
		}
		if oneOf, ok := raw["oneOf"].([]any); ok && len(oneOf) > 0 {
			return cleanSchemaNode(oneOf[0])
		}
		out["type"] = "STRING"
	}

	if out["type"] == "OBJECT" && out["properties"] == nil {
		out["properties"] = map[string]any{}
	}

	return out
}
