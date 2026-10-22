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
	seenDecls := make(map[string]bool)

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
			canonicalName := origName
			if ns != "" {
				canonicalName = fmt.Sprintf("%s__%s", ns, origName)
			}
			sanitized := SanitizeFunctionName(canonicalName)
			meta := domain.ToolMeta{
				OriginalName:  origName,
				Namespace:     ns,
				CanonicalName: canonicalName,
				IsCustom:      true,
			}
			metaMap[sanitized] = meta
			metaMap[canonicalName] = meta
			metaMap[origName] = meta
			if ns != "" {
				metaMap[ns+":"+origName] = meta
				metaMap[ns+"__"+origName] = meta
				metaMap["mcp__"+ns+"__"+origName] = meta
				metaMap["mcp__"+ns+":"+origName] = meta
				metaMap[SanitizeFunctionName(ns+":"+origName)] = meta
				metaMap[SanitizeFunctionName(ns+"__"+origName)] = meta
			}
			if seenDecls[sanitized] {
				return
			}
			seenDecls[sanitized] = true
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
			if origName == "apply_patch" {
				desc = "Use the `apply_patch` tool to create, edit, update, or delete files in the workspace. Pass the complete patch in the `input` parameter.\n" +
					"Patch syntax:\n" +
					"*** Begin Patch\n" +
					"*** Update File: <relative_path>\n" +
					"@@\n" +
					" <exact context line from file>\n" +
					"-<line to delete>\n" +
					"+<line to add>\n" +
					" <exact context line from file>\n" +
					"*** End Patch\n\n" +
					"For creating a new file:\n" +
					"*** Begin Patch\n" +
					"*** Add File: <relative_path>\n" +
					"+<line 1>\n" +
					"+<line 2>\n" +
					"*** End Patch\n\n" +
					"Rules: In hunks, every line must start with ' ' (unchanged context), '-' (delete), or '+' (add). Do NOT use diff line numbers like '@@ -1,5 +1,5 @@'."
			} else if desc == "" {
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
				OriginalName:  origName,
				Namespace:     ns,
				CanonicalName: origName,
				IsToolSearch:  true,
			}
			metaMap[sanitized] = meta
			metaMap[origName] = meta
			metaMap["toolSearch"] = meta
			if seenDecls[sanitized] {
				return
			}
			seenDecls[sanitized] = true
			params := cleanSchemaNode(t.Parameters)
			if params == nil {
				params = map[string]any{
					"type": "OBJECT",
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "STRING",
							"description": "Search query for deferred tools.",
						},
						"limit": map[string]any{
							"type":        "INTEGER",
							"description": "Maximum number of tools to return.",
						},
					},
					"required": []string{"query"},
				}
			}
			desc := t.Description
			if desc == "" {
				desc = "Search for deferred tools to load for next turn."
			}
			decls = append(decls, map[string]any{
				"name":        sanitized,
				"description": desc,
				"parameters":  params,
			})

		case "image_generation":
			origName := "image_generation"
			sanitized := "image_generation"
			meta := domain.ToolMeta{
				OriginalName:  origName,
				Namespace:     ns,
				CanonicalName: origName,
				IsImageGen:    true,
			}
			metaMap[sanitized] = meta
			metaMap[origName] = meta
			metaMap["image_gen"] = meta
			metaMap["generate_image"] = meta
			if seenDecls[sanitized] {
				return
			}
			seenDecls[sanitized] = true
			// Parameters mirror Antigravity 2.0's generate_image tool (AGY hub binary):
			// Prompt (required), ImageName (required), AspectRatio (enum, default 1:1) and
			// ImagePaths (max 3 local source/reference images). Descriptions are AGY's own text.
			params := map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"prompt": map[string]any{
						"type":        "STRING",
						"description": "The text prompt to generate an image for or the edit instructions.",
					},
					"image_name": map[string]any{
						"type":        "STRING",
						"description": "Name of the generated image to save. Should be all lowercase with underscores, describing what the image contains. Maximum 3 words. Example: 'login_page_mockup'",
					},
					"aspect_ratio": map[string]any{
						"type":        "STRING",
						"description": "Optional aspect ratio for the generated image. Supported values: '1:1', '2:3', '3:2', '3:4', '4:3', '9:16', '16:9'. Default is '1:1'.",
						"enum":        domain.SupportedImageAspectRatios,
					},
					"image_paths": map[string]any{
						"type":        "ARRAY",
						"description": "Absolute paths of local images to edit, combine, or use as references (at most 3).",
						"items":       map[string]any{"type": "STRING"},
					},
				},
				"required": []string{"prompt"},
			}
			desc := t.Description
			if desc == "" {
				desc = "Generate an image or edit existing images based on a text prompt. The resulting image will be saved as an artifact for use. You can use this tool to generate user interfaces and iterate on a design with the USER for an application or website that you are building. When creating UI designs, generate only the interface itself without surrounding device frames (laptops, phones, tablets, etc.) unless the user explicitly requests them. You can also use this tool to generate assets for use in an application or website. Pass source or reference images via image_paths (also aliased as `image_gen`)."
			}
			decls = append(decls, map[string]any{
				"name":        sanitized,
				"description": desc,
				"parameters":  params,
			})

		case "web_search":
			// Hosted server-side tools are ignored unless provided as regular functions
			return

		default:
			// Regular or MCP function
			origName := t.Name
			if origName == "" {
				return
			}
			canonicalName := origName
			if ns != "" {
				if strings.HasPrefix(ns, "mcp__") {
					canonicalName = fmt.Sprintf("%s__%s", ns, origName)
				} else if ns == "collaboration" {
					canonicalName = fmt.Sprintf("%s__%s", ns, origName)
				} else {
					canonicalName = fmt.Sprintf("mcp__%s__%s", ns, origName)
				}
			} else if strings.HasPrefix(origName, "mcp__") {
				canonicalName = origName
			}
			sanitized := SanitizeFunctionName(canonicalName)
			meta := domain.ToolMeta{
				OriginalName:  origName,
				Namespace:     ns,
				CanonicalName: canonicalName,
				IsCustom:      (origName == "apply_patch"),
				IsImageGen:    (origName == "image_generation" || origName == "generate_image" || origName == "image_gen"),
			}
			metaMap[sanitized] = meta
			metaMap[canonicalName] = meta
			metaMap[origName] = meta
			if ns != "" {
				cleanServer := strings.TrimPrefix(ns, "mcp__")
				metaMap[ns+":"+origName] = meta
				metaMap[ns+"__"+origName] = meta
				metaMap[cleanServer+":"+origName] = meta
				metaMap[cleanServer+"__"+origName] = meta
				metaMap["mcp__"+cleanServer+"__"+origName] = meta
				metaMap["mcp__"+cleanServer+":"+origName] = meta
				metaMap[SanitizeFunctionName(ns+":"+origName)] = meta
				metaMap[SanitizeFunctionName(ns+"__"+origName)] = meta
				metaMap[SanitizeFunctionName(cleanServer+":"+origName)] = meta
				metaMap[SanitizeFunctionName(cleanServer+"__"+origName)] = meta
			}
			if strings.HasPrefix(origName, "mcp__") {
				trimmed := strings.TrimPrefix(origName, "mcp__")
				parts := strings.SplitN(trimmed, "__", 2)
				if len(parts) == 2 {
					serverName, toolName := parts[0], parts[1]
					metaMap[serverName+":"+toolName] = meta
					metaMap[serverName+"__"+toolName] = meta
					metaMap["mcp__"+serverName+":"+toolName] = meta
					metaMap[SanitizeFunctionName(serverName+":"+toolName)] = meta
					metaMap[SanitizeFunctionName(serverName+"__"+toolName)] = meta
					metaMap[toolName] = meta
				}
			}
			if seenDecls[sanitized] {
				return
			}
			seenDecls[sanitized] = true
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

// DefaultCodexDesktopTools returns the core Codex Desktop tool definitions as a fallback
// when an active responses_lite session omits top-level tools on continuation turns.
func DefaultCodexDesktopTools() []domain.ToolDefinition {
	return []domain.ToolDefinition{
		{
			Type:        "function",
			Name:        "exec_command",
			Description: "Runs a command in a PTY, returning output or a session ID for ongoing interaction.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cmd":               map[string]any{"type": "string", "description": "Shell command to execute."},
					"workdir":           map[string]any{"type": "string", "description": "Optional working directory to run the command in."},
					"shell":             map[string]any{"type": "string", "description": "Shell binary to launch."},
					"yield_time_ms":     map[string]any{"type": "number", "description": "How long to wait (in milliseconds) for output before yielding."},
					"max_output_tokens": map[string]any{"type": "number", "description": "Maximum number of tokens to return."},
					"tty":               map[string]any{"type": "boolean", "description": "Whether to allocate a TTY for the command."},
				},
				"required": []any{"cmd"},
			},
		},
		{
			Type:        "function",
			Name:        "write_stdin",
			Description: "Writes characters to an existing unified_exec session and returns recent output.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id":        map[string]any{"type": "number", "description": "Identifier of the running unified_exec session."},
					"chars":             map[string]any{"type": "string", "description": "Bytes to write to stdin (may be empty to poll)."},
					"yield_time_ms":     map[string]any{"type": "number", "description": "How long to wait (in milliseconds) for output before yielding."},
					"max_output_tokens": map[string]any{"type": "number", "description": "Maximum number of tokens to return."},
				},
				"required": []any{"session_id"},
			},
		},
		{
			Type: "custom",
			Name: "apply_patch",
		},
		{
			Type:        "function",
			Name:        "update_plan",
			Description: "Updates the task plan.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"explanation": map[string]any{"type": "string"},
					"plan": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"step":   map[string]any{"type": "string"},
								"status": map[string]any{"type": "string", "enum": []any{"pending", "in_progress", "completed"}},
							},
							"required": []any{"step", "status"},
						},
					},
				},
				"required": []any{"plan"},
			},
		},
		{
			Type:        "function",
			Name:        "view_image",
			Description: "View a local image from the filesystem.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Local filesystem path to an image file."},
				},
				"required": []any{"path"},
			},
		},
		{
			Type: "tool_search",
			Name: "tool_search",
		},
		{
			Type: "image_generation",
			Name: "image_generation",
		},
	}
}
