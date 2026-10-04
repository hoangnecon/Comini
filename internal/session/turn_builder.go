package session

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"

	"comini/internal/domain"
	"comini/internal/harness"
	"comini/internal/schema"
)

func generateDeterministicSignature(sessionID, callID, funcName string) string {
	hash := sha256.Sum256([]byte("sig:" + sessionID + ":" + callID + ":" + funcName))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// DeriveSessionID derives a stable session ID from Codex thread ID or first user text.
func DeriveSessionID(parentThreadID, ownThreadID, firstUserText string) string {
	parent := strings.TrimSpace(parentThreadID)
	own := strings.TrimSpace(ownThreadID)
	var seed string
	if parent != "" && own != "" {
		seed = fmt.Sprintf("codex-thread:%s\x00%s", parent, own)
	} else if parent != "" {
		seed = fmt.Sprintf("codex-thread:%s", parent)
	} else if own != "" {
		seed = fmt.Sprintf("codex-thread:%s", own)
	} else if firstUserText != "" {
		seed = strings.TrimSpace(firstUserText)
	}

	if seed == "" {
		return fmt.Sprintf("-%d", rand.Int63()&0x7fffffffffffffff)
	}

	digest := sha256.Sum256([]byte(seed))
	val := binary.BigEndian.Uint64(digest[:8]) & 0x7fffffffffffffff
	return fmt.Sprintf("-%d", val)
}

func candidateNames(name string) []string {
	var candidates []string
	seen := make(map[string]bool)
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n != "" && !seen[n] {
			seen[n] = true
			candidates = append(candidates, n)
		}
	}

	add(name)
	add(schema.SanitizeFunctionName(name))
	if idx := strings.LastIndex(name, ":"); idx != -1 {
		add(name[idx+1:])
	}
	add(strings.ReplaceAll(name, ":", "_"))
	return candidates
}

func detectMimeType(b64 string) string {
	b64 = strings.TrimSpace(b64)
	if strings.HasPrefix(b64, "iVBORw0KGgo") {
		return "image/png"
	}
	if strings.HasPrefix(b64, "/9j/") {
		return "image/jpeg"
	}
	if strings.HasPrefix(b64, "UklGR") {
		return "image/webp"
	}
	if strings.HasPrefix(b64, "R0lGOD") {
		return "image/gif"
	}
	return "image/png"
}

func extractDataURL(s string) (string, string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "data:image/") {
		return "", "", false
	}
	idx := strings.Index(s, ";base64,")
	if idx <= 5 {
		return "", "", false
	}
	mime := s[5:idx]
	data := strings.TrimSpace(s[idx+8:])
	return mime, data, true
}

func tryLoadLocalImage(path string) (*domain.GeminiInlineData, bool) {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "file://") {
		path = strings.TrimPrefix(path, "file://")
	}
	lower := strings.ToLower(path)
	if !strings.HasSuffix(lower, ".png") && !strings.HasSuffix(lower, ".jpg") &&
		!strings.HasSuffix(lower, ".jpeg") && !strings.HasSuffix(lower, ".webp") &&
		!strings.HasSuffix(lower, ".gif") {
		return nil, false
	}

	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 || info.Size() > 20*1024*1024 {
		return nil, false
	}

	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}

	mime := "image/png"
	if strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") {
		mime = "image/jpeg"
	} else if strings.HasSuffix(lower, ".webp") {
		mime = "image/webp"
	} else if strings.HasSuffix(lower, ".gif") {
		mime = "image/gif"
	}

	b64 := base64.StdEncoding.EncodeToString(bytes)
	return &domain.GeminiInlineData{MimeType: mime, Data: b64}, true
}

func extractInlineDataFromText(txt string) ([]*domain.GeminiInlineData, string) {
	var images []*domain.GeminiInlineData
	cleaned := txt

	for {
		idx := strings.Index(cleaned, "data:image/")
		if idx == -1 {
			break
		}
		semiIdx := strings.Index(cleaned[idx:], ";base64,")
		if semiIdx == -1 || semiIdx > 40 {
			break
		}
		mime := cleaned[idx+5 : idx+semiIdx]
		dataStart := idx + semiIdx + 8

		end := dataStart
		for end < len(cleaned) {
			c := cleaned[end]
			if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
				c == '+' || c == '/' || c == '=' || c == '\r' || c == '\n' {
				end++
			} else {
				break
			}
		}

		rawB64 := strings.ReplaceAll(strings.ReplaceAll(cleaned[dataStart:end], "\r", ""), "\n", "")
		if len(rawB64) > 20 {
			images = append(images, &domain.GeminiInlineData{
				MimeType: mime,
				Data:     rawB64,
			})
			cleaned = cleaned[:idx] + fmt.Sprintf("[Attached Image: %s]", mime) + cleaned[end:]
		} else {
			break
		}
	}

	trimmed := strings.TrimSpace(cleaned)
	if len(trimmed) > 200 {
		mime := ""
		if strings.HasPrefix(trimmed, "iVBORw0KGgo") {
			mime = "image/png"
		} else if strings.HasPrefix(trimmed, "/9j/") {
			mime = "image/jpeg"
		} else if strings.HasPrefix(trimmed, "UklGR") {
			mime = "image/webp"
		} else if strings.HasPrefix(trimmed, "R0lGOD") {
			mime = "image/gif"
		}
		if mime != "" {
			images = append(images, &domain.GeminiInlineData{
				MimeType: mime,
				Data:     trimmed,
			})
			cleaned = fmt.Sprintf("[Attached Image: %s]", mime)
		}
	}

	// Only attempt local image loading if the text is short (< 500 chars) and explicitly mentions a single image file path
	if len(images) == 0 && len(cleaned) < 500 {
		words := strings.Fields(cleaned)
		if len(words) <= 30 {
			for _, w := range words {
				cleanW := strings.Trim(w, "\"'`(),;:<>[]{}*")
				if strings.HasPrefix(cleanW, "/") || strings.HasPrefix(cleanW, "file://") {
					if inData, ok := tryLoadLocalImage(cleanW); ok {
						images = append(images, inData)
						cleaned = strings.Replace(cleaned, cleanW, fmt.Sprintf("[Attached Local Image: %s (%s)]", cleanW, inData.MimeType), 1)
						break // Load at most 1 local image from a single text prompt
					}
				}
			}
		}
	}

	return images, cleaned
}

func extractInlineDataFromMap(m map[string]any) ([]*domain.GeminiInlineData, map[string]any) {
	var images []*domain.GeminiInlineData
	cleanMap := make(map[string]any)

	for k, v := range m {
		switch val := v.(type) {
		case string:
			if mime, data, ok := extractDataURL(val); ok {
				images = append(images, &domain.GeminiInlineData{MimeType: mime, Data: data})
				cleanMap[k] = fmt.Sprintf("[Attached Image: %s loaded into vision encoder]", mime)
				continue
			}
			if len(val) > 200 && (strings.HasPrefix(val, "iVBORw0KGgo") || strings.HasPrefix(val, "/9j/") || strings.HasPrefix(val, "UklGR") || strings.HasPrefix(val, "R0lGOD")) {
				mime := detectMimeType(val)
				images = append(images, &domain.GeminiInlineData{MimeType: mime, Data: val})
				cleanMap[k] = fmt.Sprintf("[Attached Image: %s loaded into vision encoder]", mime)
				continue
			}
			if strings.Contains(val, "data:image/") {
				imgs, cleaned := extractInlineDataFromText(val)
				if len(imgs) > 0 {
					images = append(images, imgs...)
					cleanMap[k] = cleaned
					continue
				}
			}
			cleanMap[k] = val

		case map[string]any:
			if k == "image_url" || k == "url" {
				if u, ok := val["url"].(string); ok {
					if mime, data, ok := extractDataURL(u); ok {
						images = append(images, &domain.GeminiInlineData{MimeType: mime, Data: data})
						cleanMap[k] = fmt.Sprintf("[Attached Image: %s loaded into vision encoder]", mime)
						continue
					}
				}
			}
			if k == "source" {
				if data, ok := val["data"].(string); ok && len(data) > 50 {
					mime, _ := val["media_type"].(string)
					if mime == "" {
						mime, _ = val["mediaType"].(string)
					}
					if mime == "" {
						mime = detectMimeType(data)
					}
					images = append(images, &domain.GeminiInlineData{MimeType: mime, Data: data})
					cleanMap[k] = fmt.Sprintf("[Attached Image: %s loaded into vision encoder]", mime)
					continue
				}
			}
			subImgs, subClean := extractInlineDataFromMap(val)
			if len(subImgs) > 0 {
				images = append(images, subImgs...)
				cleanMap[k] = subClean
			} else {
				cleanMap[k] = val
			}

		case []any:
			var cleanList []any
			for _, elem := range val {
				if elemMap, ok := elem.(map[string]any); ok {
					subImgs, subClean := extractInlineDataFromMap(elemMap)
					if len(subImgs) > 0 {
						images = append(images, subImgs...)
						cleanList = append(cleanList, subClean)
					} else {
						cleanList = append(cleanList, elem)
					}
				} else if elemStr, ok := elem.(string); ok {
					subImgs, subClean := extractInlineDataFromText(elemStr)
					if len(subImgs) > 0 {
						images = append(images, subImgs...)
						cleanList = append(cleanList, subClean)
					} else {
						cleanList = append(cleanList, elem)
					}
				} else {
					cleanList = append(cleanList, elem)
				}
			}
			cleanMap[k] = cleanList

		default:
			cleanMap[k] = v
		}
	}

	if b64, ok := m["data"].(string); ok && len(b64) > 50 {
		mime, _ := m["mime_type"].(string)
		if mime == "" {
			mime, _ = m["mimeType"].(string)
		}
		if mime == "" {
			mime = detectMimeType(b64)
		}
		if strings.HasPrefix(mime, "image/") {
			alreadyExtracted := false
			for _, img := range images {
				if img.Data == b64 {
					alreadyExtracted = true
					break
				}
			}
			if !alreadyExtracted {
				images = append(images, &domain.GeminiInlineData{MimeType: mime, Data: b64})
			}
			cleanMap["data"] = fmt.Sprintf("[Attached Image: %s loaded into vision encoder]", mime)
		}
	}

	return images, cleanMap
}

func extractImagesAndCleanOutput(rawOutput json.RawMessage) ([]*domain.GeminiInlineData, map[string]any) {
	if len(rawOutput) == 0 {
		return nil, map[string]any{"result": ""}
	}

	var m map[string]any
	if err := json.Unmarshal(rawOutput, &m); err == nil {
		imgs, cleanMap := extractInlineDataFromMap(m)
		if len(imgs) > 0 {
			cleanMap["image_status"] = "Image loaded into vision encoder"
		}
		return imgs, cleanMap
	}

	var list []any
	if err := json.Unmarshal(rawOutput, &list); err == nil {
		var allImgs []*domain.GeminiInlineData
		var cleanList []any
		for _, item := range list {
			if itemMap, ok := item.(map[string]any); ok {
				imgs, cleanMap := extractInlineDataFromMap(itemMap)
				allImgs = append(allImgs, imgs...)
				cleanList = append(cleanList, cleanMap)
			} else if itemStr, ok := item.(string); ok {
				imgs, cleaned := extractInlineDataFromText(itemStr)
				allImgs = append(allImgs, imgs...)
				cleanList = append(cleanList, cleaned)
			} else {
				cleanList = append(cleanList, item)
			}
		}
		cleanMap := map[string]any{"content": cleanList}
		if len(allImgs) > 0 {
			cleanMap["image_status"] = "Image loaded into vision encoder"
		}
		return allImgs, cleanMap
	}

	var strOut string
	if err := json.Unmarshal(rawOutput, &strOut); err == nil {
		trimmed := strings.TrimSpace(strOut)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			return extractImagesAndCleanOutput([]byte(trimmed))
		}
		imgs, cleaned := extractInlineDataFromText(strOut)
		cleanMap := map[string]any{"result": cleaned}
		if len(imgs) > 0 {
			cleanMap["image_status"] = "Image loaded into vision encoder"
		}
		return imgs, cleanMap
	}

	rawStr := string(rawOutput)
	imgs, cleaned := extractInlineDataFromText(rawStr)
	cleanMap := map[string]any{"result": cleaned}
	if len(imgs) > 0 {
		cleanMap["image_status"] = "Image loaded into vision encoder"
	}
	return imgs, cleanMap
}

func extractPartsFromContent(rawContent json.RawMessage) []domain.GeminiPart {
	if len(rawContent) == 0 {
		return nil
	}

	var result []domain.GeminiPart

	// 1. Try parsing as array of maps
	var rawList []map[string]any
	if err := json.Unmarshal(rawContent, &rawList); err == nil && len(rawList) > 0 {
		for _, m := range rawList {
			imgs, cleanMap := extractInlineDataFromMap(m)
			for _, img := range imgs {
				result = append(result, domain.GeminiPart{InlineData: img})
			}
			if txt, ok := cleanMap["text"].(string); ok && strings.TrimSpace(txt) != "" {
				cleanTxt := harness.SanitizeMessageText(txt)
				if cleanTxt != "" {
					result = append(result, domain.GeminiPart{Text: cleanTxt})
				}
			} else if txt, ok := cleanMap["content"].(string); ok && strings.TrimSpace(txt) != "" {
				cleanTxt := harness.SanitizeMessageText(txt)
				if cleanTxt != "" {
					result = append(result, domain.GeminiPart{Text: cleanTxt})
				}
			}
		}
		if len(result) > 0 {
			return result
		}
	}

	// 2. Try parsing as single map
	var singleMap map[string]any
	if err := json.Unmarshal(rawContent, &singleMap); err == nil && len(singleMap) > 0 {
		imgs, cleanMap := extractInlineDataFromMap(singleMap)
		for _, img := range imgs {
			result = append(result, domain.GeminiPart{InlineData: img})
		}
		if txt, ok := cleanMap["text"].(string); ok && strings.TrimSpace(txt) != "" {
			cleanTxt := harness.SanitizeMessageText(txt)
			if cleanTxt != "" {
				result = append(result, domain.GeminiPart{Text: cleanTxt})
			}
		} else if txt, ok := cleanMap["content"].(string); ok && strings.TrimSpace(txt) != "" {
			cleanTxt := harness.SanitizeMessageText(txt)
			if cleanTxt != "" {
				result = append(result, domain.GeminiPart{Text: cleanTxt})
			}
		}
		if len(result) > 0 {
			return result
		}
	}

	// 3. Try parsing as string
	var textContent string
	if err := json.Unmarshal(rawContent, &textContent); err == nil {
		trimmed := strings.TrimSpace(textContent)
		if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
			if nestedParts := extractPartsFromContent([]byte(trimmed)); len(nestedParts) > 0 {
				return nestedParts
			}
		}

		imgs, cleaned := extractInlineDataFromText(textContent)
		for _, img := range imgs {
			result = append(result, domain.GeminiPart{InlineData: img})
		}
		cleanTxt := harness.SanitizeMessageText(cleaned)
		if strings.TrimSpace(cleanTxt) != "" {
			result = append(result, domain.GeminiPart{Text: cleanTxt})
		}
		return result
	}

	// 4. Raw string fallback
	rawStr := string(rawContent)
	imgs, cleaned := extractInlineDataFromText(rawStr)
	for _, img := range imgs {
		result = append(result, domain.GeminiPart{InlineData: img})
	}
	cleanTxt := harness.SanitizeMessageText(cleaned)
	if strings.TrimSpace(cleanTxt) != "" {
		result = append(result, domain.GeminiPart{Text: cleanTxt})
	}

	return result
}

func sanitizeArgsMap(m map[string]any) map[string]any {
	cleaned := make(map[string]any)
	for k, v := range m {
		switch val := v.(type) {
		case string:
			if len(val) > 200 && (strings.HasPrefix(val, "data:image/") || strings.HasPrefix(val, "iVBORw0KGgo") || strings.HasPrefix(val, "/9j/") || strings.HasPrefix(val, "UklGR")) {
				cleaned[k] = fmt.Sprintf("[Attached Image Data: %d bytes omitted from text prompt]", len(val))
			} else if len(val) > 2000000 {
				cleaned[k] = val[:1500000] + "\n...[truncated for context budget]...\n" + val[len(val)-400000:]
			} else {
				cleaned[k] = val
			}
		case map[string]any:
			cleaned[k] = sanitizeArgsMap(val)
		default:
			cleaned[k] = v
		}
	}
	return cleaned
}

func mergeConsecutiveRoles(contents []domain.GeminiContent) []domain.GeminiContent {
	if len(contents) <= 1 {
		return contents
	}
	var merged []domain.GeminiContent
	for _, c := range contents {
		if len(c.Parts) == 0 {
			continue
		}
		if len(merged) > 0 && merged[len(merged)-1].Role == c.Role {
			merged[len(merged)-1].Parts = append(merged[len(merged)-1].Parts, c.Parts...)
		} else {
			merged = append(merged, c)
		}
	}
	return merged
}

func capAllPartsText(contents []domain.GeminiContent) []domain.GeminiContent {
	const maxSinglePartChars = 1000000 // 1MB max per text part

	// Retain at most the last 3 inline images across the entire conversation history
	imageCount := 0
	for ci := len(contents) - 1; ci >= 0; ci-- {
		for pi := range contents[ci].Parts {
			p := &contents[ci].Parts[pi]
			if p.InlineData != nil {
				imageCount++
				if imageCount > 3 {
					p.InlineData = nil
					if p.Text == "" {
						p.Text = "[Image omitted from historical context]"
					}
				}
			}
			// 1. Cap Text parts
			if len(p.Text) > maxSinglePartChars {
				head := p.Text[:700000]
				tail := p.Text[len(p.Text)-200000:]
				p.Text = fmt.Sprintf("%s\n\n...[Text truncated %d bytes for context budget]...\n\n%s", head, len(p.Text)-900000, tail)
			}
			// 2. Cap FunctionResponse parts
			if p.FunctionResponse != nil && p.FunctionResponse.Response != nil {
				b, err := json.Marshal(p.FunctionResponse.Response)
				if err == nil && len(b) > maxSinglePartChars {
					p.FunctionResponse.Response = map[string]any{
						"result": string(b[:700000]) + "\n...[Output truncated for context budget]...\n" + string(b[len(b)-200000:]),
					}
				}
			}
		}
	}
	return contents
}

func sanitizeAndValidateGeminiContents(contents []domain.GeminiContent) []domain.GeminiContent {
	if len(contents) == 0 {
		return []domain.GeminiContent{
			{Role: "user", Parts: []domain.GeminiPart{{Text: "Hello"}}},
		}
	}

	// 1. Clean empty parts inside each turn
	var cleanContents []domain.GeminiContent
	for _, c := range contents {
		var validParts []domain.GeminiPart
		for _, p := range c.Parts {
			if p.Text != "" || p.FunctionCall != nil || p.FunctionResponse != nil || p.InlineData != nil {
				validParts = append(validParts, p)
			}
		}
		if len(validParts) > 0 {
			cleanContents = append(cleanContents, domain.GeminiContent{
				Role:  c.Role,
				Parts: validParts,
			})
		}
	}

	if len(cleanContents) == 0 {
		return []domain.GeminiContent{
			{Role: "user", Parts: []domain.GeminiPart{{Text: "Hello"}}},
		}
	}

	// 2. Merge consecutive roles
	cleanContents = mergeConsecutiveRoles(cleanContents)

	// 3. Ensure alternating roles and validate FunctionCall <-> FunctionResponse pairs
	for i := 0; i < len(cleanContents); i++ {
		curr := &cleanContents[i]

		if curr.Role == "model" {
			for pi := range curr.Parts {
				p := &curr.Parts[pi]
				if p.FunctionCall != nil {
					// Cloud Code Assist strictly requires thoughtSignature on FunctionCall parts.
					// If missing or name is empty, downgrade to plain text action to avoid HTTP 400.
					if p.FunctionCall.Name == "" || p.ThoughtSignature == "" {
						var argsStr string
						if p.FunctionCall.Args != nil {
							b, _ := json.Marshal(p.FunctionCall.Args)
							argsStr = string(b)
						}
						p.Text = fmt.Sprintf("[Action: called %s with %s]", p.FunctionCall.Name, argsStr)
						p.FunctionCall = nil
						p.ThoughtSignature = ""
					}
				}
			}
		} else if curr.Role == "user" {
			var prevModelCalls map[string]bool
			if i > 0 && cleanContents[i-1].Role == "model" {
				prevModelCalls = make(map[string]bool)
				for _, p := range cleanContents[i-1].Parts {
					if p.FunctionCall != nil && p.FunctionCall.Name != "" && p.ThoughtSignature != "" {
						prevModelCalls[p.FunctionCall.Name] = true
					}
				}
			}

			for pi := range curr.Parts {
				p := &curr.Parts[pi]
				if p.FunctionResponse != nil {
					if prevModelCalls == nil || !prevModelCalls[p.FunctionResponse.Name] {
						respJSON, _ := json.Marshal(p.FunctionResponse.Response)
						p.Text = fmt.Sprintf("[Observation: %s returned %s]", p.FunctionResponse.Name, string(respJSON))
						p.FunctionResponse = nil
					}
				}
			}
		}
	}

	// 4. Ensure first turn is 'user'
	if len(cleanContents) > 0 && cleanContents[0].Role != "user" {
		cleanContents = append([]domain.GeminiContent{
			{Role: "user", Parts: []domain.GeminiPart{{Text: "Hello"}}},
		}, cleanContents...)
	}

	// 5. Ensure last turn is 'user'
	if len(cleanContents) > 0 && cleanContents[len(cleanContents)-1].Role != "user" {
		cleanContents = append(cleanContents, domain.GeminiContent{
			Role:  "user",
			Parts: []domain.GeminiPart{{Text: "Please continue."}},
		})
	}

	return mergeConsecutiveRoles(cleanContents)
}

func isUserConversationalContent(c domain.GeminiContent) bool {
	if c.Role != "user" {
		return false
	}
	for _, p := range c.Parts {
		if p.InlineData != nil {
			return true
		}
		if p.Text != "" {
			trimmed := strings.TrimSpace(p.Text)
			// Explicitly preserve memory citations, active technical plans, thread goals, and confirmed decisions
			if strings.Contains(trimmed, "<oai-mem-citation>") ||
				strings.Contains(trimmed, "[Active Technical Plan]") ||
				strings.Contains(trimmed, "[User Confirmed") ||
				strings.Contains(trimmed, "[Updated Thread Goal]") ||
				strings.Contains(trimmed, "MEMORY_SUMMARY") {
				return true
			}
			// A genuine user instruction is not a tool observation or system compaction note
			if !strings.HasPrefix(trimmed, "[Observation:") &&
				!strings.HasPrefix(trimmed, "[Note:") {
				return true
			}
		}
	}
	return false
}

func compressHistoricalToolOutputs(contents []domain.GeminiContent, keepRecentCount int) []domain.GeminiContent {
	cutoff := len(contents) - keepRecentCount
	if cutoff <= 0 {
		return contents
	}

	for i := 0; i < cutoff; i++ {
		for pi := range contents[i].Parts {
			p := &contents[i].Parts[pi]
			// Compress old FunctionResponse if large (> 2000 chars)
			if p.FunctionResponse != nil && p.FunctionResponse.Response != nil {
				b, err := json.Marshal(p.FunctionResponse.Response)
				if err == nil && len(b) > 2000 {
					head := string(b[:1000])
					tail := string(b[len(b)-500:])
					p.FunctionResponse.Response = map[string]any{
						"result": head + "\n...[Earlier tool output truncated for context budget]...\n" + tail,
					}
				}
			}
			// Compress old [Observation: ...] text if large (> 2000 chars)
			if strings.HasPrefix(strings.TrimSpace(p.Text), "[Observation:") && len(p.Text) > 2000 {
				head := p.Text[:1000]
				tail := p.Text[len(p.Text)-500:]
				p.Text = head + "\n...[Earlier observation truncated for context budget]...\n" + tail
			}
		}
	}
	return contents
}

func enforceContextBudgetOnContents(contents []domain.GeminiContent) []domain.GeminiContent {
	contents = capAllPartsText(contents)
	contents = sanitizeAndValidateGeminiContents(contents)

	calcTextLen := func(cts []domain.GeminiContent) int {
		total := 0
		for _, c := range cts {
			for _, p := range c.Parts {
				total += len(p.Text)
				if p.FunctionCall != nil {
					b, _ := json.Marshal(p.FunctionCall.Args)
					total += len(b)
				}
				if p.FunctionResponse != nil {
					b, _ := json.Marshal(p.FunctionResponse.Response)
					total += len(b)
				}
			}
		}
		return total
	}

	// 1. Initial check: 6MB text budget and 500 turns ensures request fits within Google CCA 20MB limit
	const maxCharsBudget = 6000000
	const maxTurnsBudget = 500

	if calcTextLen(contents) <= maxCharsBudget && len(contents) <= maxTurnsBudget {
		return contents
	}

	// 2. Tier 1 Compression: compress older tool outputs (older than last 40 turns)
	contents = compressHistoricalToolOutputs(contents, 40)
	if calcTextLen(contents) <= maxCharsBudget && len(contents) <= maxTurnsBudget {
		return sanitizeAndValidateGeminiContents(contents)
	}

	// 3. Tier 2: If still exceeding budget, preserve Turn 0, ALL historical user conversational prompts,
	// and the last 150 recent turns in full.
	const keepRecentTurns = 150
	if len(contents) > keepRecentTurns+1 {
		cutoffIndex := len(contents) - keepRecentTurns
		first := contents[0]
		recent := contents[cutoffIndex:]

		var preservedUserTurns []domain.GeminiContent
		for i := 1; i < cutoffIndex; i++ {
			if isUserConversationalContent(contents[i]) {
				preservedUserTurns = append(preservedUserTurns, contents[i])
			}
		}

		summaryPart := domain.GeminiPart{
			Text: fmt.Sprintf("[Note: %d earlier intermediate tool execution turns pruned for context budget; all user conversation instructions preserved.]", cutoffIndex-1-len(preservedUserTurns)),
		}

		newContents := []domain.GeminiContent{first}
		newContents = append(newContents, preservedUserTurns...)
		newContents = append(newContents, domain.GeminiContent{Role: "user", Parts: []domain.GeminiPart{summaryPart}})
		newContents = append(newContents, recent...)
		contents = newContents
	}

	return sanitizeAndValidateGeminiContents(contents)
}

// BuildGeminiWireBody converts incoming ResponsesRequest to Gemini Wire format.
func BuildGeminiWireBody(
	req *domain.ResponsesRequest,
	sessionID string,
	funcDecls []map[string]any,
	thinkingLevel string,
	store *SignatureStore,
) domain.GeminiWireBody {
	var contents []domain.GeminiContent

	// 1. Collect tool names and sanitize System Instruction
	toolNames := make([]string, 0, len(funcDecls))
	for _, decl := range funcDecls {
		if name, ok := decl["name"].(string); ok && name != "" {
			toolNames = append(toolNames, name)
		}
	}

	sanitizedSys := harness.SanitizeSystemInstruction(req.Instructions, toolNames)
	var sysInst *domain.GeminiSystemInstruction
	if sanitizedSys != nil {
		txt := *sanitizedSys
		if len(txt) > 2000000 {
			txt = txt[:2000000]
		}
		sysInst = &domain.GeminiSystemInstruction{
			Parts: []domain.GeminiPart{{Text: txt}},
		}
	}

	// 2. Parse Input Items
	var items []domain.InputItem
	if len(req.Input) > 0 {
		_ = json.Unmarshal(req.Input, &items)
	}

	var currentRole string
	var currentParts []domain.GeminiPart

	flushTurn := func() {
		if currentRole != "" && len(currentParts) > 0 {
			contents = append(contents, domain.GeminiContent{
				Role:  currentRole,
				Parts: currentParts,
			})
			currentParts = nil
			currentRole = ""
		}
	}

	untrackedCalls := make(map[string]bool)

	normalizeType := func(t string) string {
		t = strings.ToLower(strings.TrimSpace(t))
		t = strings.ReplaceAll(t, "_", "")
		t = strings.ReplaceAll(t, "-", "")
		return t
	}

	// Pre-index all tool calls by CallID/ID to maintain full context for observations
	type ToolCallInfo struct {
		Name      string
		Arguments string
		Input     string
	}
	callInfoMap := make(map[string]ToolCallInfo)
	for _, it := range items {
		norm := normalizeType(it.Type)
		switch norm {
		case "functioncall", "websearchcall", "imagegenerationcall", "toolsearchcall", "customtoolcall", "localshellcall", "mcptoolcall", "dynamictoolcall", "collabagenttoolcall":
			cid := it.CallID
			if cid == "" {
				cid = it.ID
			}
			name := it.Name
			if name == "" && it.Tool != "" {
				name = it.Tool
			}
			if name == "" && norm == "localshellcall" {
				name = "exec_command"
			}
			args := it.Arguments
			if args == "" {
				args = it.Input
			}
			if cid != "" {
				info := ToolCallInfo{
					Name:      name,
					Arguments: args,
					Input:     it.Input,
				}
				callInfoMap[cid] = info
				if it.ID != "" && it.ID != cid {
					callInfoMap[it.ID] = info
				}
			}
		}
	}

	for _, item := range items {
		normType := normalizeType(item.Type)

		switch normType {
		case "additionaltools", "compactiontrigger", "reasoning":
			continue

		case "compaction":
			var compText string
			if err := json.Unmarshal(item.Content, &compText); err == nil && strings.TrimSpace(compText) != "" {
				if currentRole != "" && currentRole != "user" {
					flushTurn()
				}
				currentRole = "user"
				currentParts = append(currentParts, domain.GeminiPart{
					Text: fmt.Sprintf("[Previous Conversation Compaction Summary]:\n%s", compText),
				})
			} else if len(item.Output) > 0 {
				var outText string
				if err := json.Unmarshal(item.Output, &outText); err == nil && strings.TrimSpace(outText) != "" {
					if currentRole != "" && currentRole != "user" {
						flushTurn()
					}
					currentRole = "user"
					currentParts = append(currentParts, domain.GeminiPart{
						Text: fmt.Sprintf("[Previous Conversation Compaction Summary]:\n%s", outText),
					})
				}
			}

		case "retainedcontext", "verifiedanswer":
			if currentRole != "" && currentRole != "user" {
				flushTurn()
			}
			currentRole = "user"

			var payloadStruct struct {
				Type      string `json:"type"`
				Questions []struct {
					Question string `json:"question"`
					Answer   string `json:"answer"`
				} `json:"questions"`
			}
			if len(item.Payload) > 0 {
				_ = json.Unmarshal(item.Payload, &payloadStruct)
			}
			if len(payloadStruct.Questions) == 0 && len(item.Content) > 0 {
				_ = json.Unmarshal(item.Content, &payloadStruct)
			}

			if len(payloadStruct.Questions) > 0 {
				var sb strings.Builder
				sb.WriteString("[User Confirmed Decisions & Answers]:\n")
				for _, q := range payloadStruct.Questions {
					sb.WriteString(fmt.Sprintf("- Q: %s\n  A: %s\n", q.Question, q.Answer))
				}
				currentParts = append(currentParts, domain.GeminiPart{Text: strings.TrimSpace(sb.String())})
			} else if item.Text != "" {
				currentParts = append(currentParts, domain.GeminiPart{Text: fmt.Sprintf("[User Confirmed Context]: %s", item.Text)})
			}

		case "plan":
			if currentRole != "" && currentRole != "user" {
				flushTurn()
			}
			currentRole = "user"

			var planText string
			if item.Text != "" {
				planText = item.Text
			} else if len(item.Content) > 0 {
				_ = json.Unmarshal(item.Content, &planText)
			}
			if planText == "" && len(item.Payload) > 0 {
				var pMap map[string]any
				if err := json.Unmarshal(item.Payload, &pMap); err == nil {
					if txt, ok := pMap["text"].(string); ok {
						planText = txt
					} else if plan, ok := pMap["plan"].(string); ok {
						planText = plan
					}
				}
			}
			if planText != "" {
				currentParts = append(currentParts, domain.GeminiPart{
					Text: fmt.Sprintf("[Active Technical Plan]:\n%s", strings.TrimSpace(planText)),
				})
			}

		case "filechange":
			if currentRole != "" && currentRole != "user" {
				flushTurn()
			}
			currentRole = "user"

			var changeSummary string
			if item.Stdout != "" {
				changeSummary = item.Stdout
			} else if len(item.Payload) > 0 {
				var pMap map[string]any
				if err := json.Unmarshal(item.Payload, &pMap); err == nil {
					if out, ok := pMap["stdout"].(string); ok && out != "" {
						changeSummary = out
					} else if files, ok := pMap["files"].(map[string]any); ok && len(files) > 0 {
						var fileNames []string
						for f := range files {
							fileNames = append(fileNames, f)
						}
						changeSummary = fmt.Sprintf("Updated files: %s", strings.Join(fileNames, ", "))
					}
				}
			}
			if changeSummary != "" {
				currentParts = append(currentParts, domain.GeminiPart{
					Text: fmt.Sprintf("[Workspace File Changes]:\n%s", strings.TrimSpace(changeSummary)),
				})
			}

		case "threadgoalupdated":
			if currentRole != "" && currentRole != "user" {
				flushTurn()
			}
			currentRole = "user"

			goalText := item.Goal
			if goalText == "" && len(item.Payload) > 0 {
				var gMap map[string]any
				if err := json.Unmarshal(item.Payload, &gMap); err == nil {
					if g, ok := gMap["goal"].(string); ok {
						goalText = g
					}
				}
			}
			if goalText != "" {
				currentParts = append(currentParts, domain.GeminiPart{
					Text: fmt.Sprintf("[Updated Thread Goal]: %s", strings.TrimSpace(goalText)),
				})
			}

		case "worldstate":
			if len(item.Payload) > 0 {
				var wMap map[string]any
				if err := json.Unmarshal(item.Payload, &wMap); err == nil {
					if state, ok := wMap["state"].(map[string]any); ok {
						if cm, ok := state["collaboration_mode"].(map[string]any); ok {
							if mode, ok := cm["mode"].(string); ok && mode != "" {
								if currentRole != "" && currentRole != "user" {
									flushTurn()
								}
								currentRole = "user"
								currentParts = append(currentParts, domain.GeminiPart{
									Text: fmt.Sprintf("[Active Collaboration Mode: %s]", mode),
								})
							}
						}
					}
				}
			}

		case "agentmessage":
			if currentRole != "" && currentRole != "model" {
				flushTurn()
			}
			currentRole = "model"

			if len(item.Content) > 0 {
				parsedParts := extractPartsFromContent(item.Content)
				if len(parsedParts) > 0 {
					currentParts = append(currentParts, parsedParts...)
				}
			} else if item.Text != "" {
				imgs, cleaned := extractInlineDataFromText(item.Text)
				for _, img := range imgs {
					currentParts = append(currentParts, domain.GeminiPart{InlineData: img})
				}
				cleanTxt := harness.SanitizeMessageText(cleaned)
				if strings.TrimSpace(cleanTxt) != "" {
					currentParts = append(currentParts, domain.GeminiPart{Text: cleanTxt})
				}
			}

		case "message", "usermessage", "text", "inputtext", "":
			role := "user"
			if item.Role == "assistant" {
				role = "model"
			}

			if currentRole != "" && currentRole != role {
				flushTurn()
			}
			currentRole = role

			if len(item.Content) > 0 {
				parsedParts := extractPartsFromContent(item.Content)
				if len(parsedParts) > 0 {
					currentParts = append(currentParts, parsedParts...)
				}
			} else if item.Text != "" {
				imgs, cleaned := extractInlineDataFromText(item.Text)
				for _, img := range imgs {
					currentParts = append(currentParts, domain.GeminiPart{InlineData: img})
				}
				cleanTxt := harness.SanitizeMessageText(cleaned)
				if strings.TrimSpace(cleanTxt) != "" {
					currentParts = append(currentParts, domain.GeminiPart{Text: cleanTxt})
				}
			}

		case "imageview":
			if currentRole != "" && currentRole != "user" {
				flushTurn()
			}
			currentRole = "user"
			if item.Path != "" {
				if inData, ok := tryLoadLocalImage(item.Path); ok {
					currentParts = append(currentParts, domain.GeminiPart{InlineData: inData})
					currentParts = append(currentParts, domain.GeminiPart{Text: fmt.Sprintf("[Attached Local Image: %s (%s)]", item.Path, inData.MimeType)})
				} else {
					currentParts = append(currentParts, domain.GeminiPart{Text: fmt.Sprintf("[Attached Image Path: %s]", item.Path)})
				}
			}

		case "commandexecution":
			outText := item.AggregatedOutput
			if outText == "" {
				outText = item.Stdout
			}
			if outText != "" {
				if currentRole != "" && currentRole != "user" {
					flushTurn()
				}
				currentRole = "user"
				currentParts = append(currentParts, domain.GeminiPart{Text: fmt.Sprintf("[Observation: command %s executed -> output: %s]", item.Command, outText)})
			}

		case "mcptoolcall", "dynamictoolcall", "collabagenttoolcall":
			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}
			callName := item.Tool
			if callName == "" {
				callName = item.Name
			}
			if item.Server != "" {
				callName = fmt.Sprintf("%s:%s", item.Server, callName)
			}
			if callID != "" {
				untrackedCalls[callID] = true
			}

		case "functioncall", "websearchcall", "imagegenerationcall", "toolsearchcall":
			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}
			if callID != "" {
				untrackedCalls[callID] = true
			}

		case "customtoolcall":
			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}
			if callID != "" {
				untrackedCalls[callID] = true
			}

		case "localshellcall":
			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}
			if callID != "" {
				untrackedCalls[callID] = true
			}

		case "functioncalloutput", "customtoolcalloutput", "localshellcalloutput", "localshelloutput", "mcptoolcalloutput", "toolsearchoutput":
			if currentRole != "" && currentRole != "user" {
				flushTurn()
			}
			currentRole = "user"

			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}

			info, hasInfo := callInfoMap[callID]
			funcName := item.Name
			if funcName == "" && hasInfo && info.Name != "" {
				funcName = info.Name
			}
			if funcName == "" && store != nil {
				if _, storedName, ok := store.Get(callID); ok && storedName != "" {
					funcName = storedName
				} else if item.ID != "" {
					if _, storedName, ok := store.Get(item.ID); ok && storedName != "" {
						funcName = storedName
					}
				}
			}
			if funcName == "" {
				funcName = callID
			}
			sanitizedName := schema.SanitizeFunctionName(funcName)

			extractedImages, cleanResp := extractImagesAndCleanOutput(item.Output)

			// 1. Attach native Vision inlineData parts first
			for _, img := range extractedImages {
				currentParts = append(currentParts, domain.GeminiPart{InlineData: img})
			}

			// 2. Attach clean observation with full tool execution context
			if len(extractedImages) > 0 {
				currentParts = append(currentParts, domain.GeminiPart{
					Text: fmt.Sprintf("[Observation: tool %s returned visual image (%s)]", sanitizedName, extractedImages[0].MimeType),
				})
			} else {
				respStr, _ := json.Marshal(cleanResp)
				if hasInfo && info.Input != "" && (sanitizedName == "apply_patch" || info.Name == "apply_patch") {
					currentParts = append(currentParts, domain.GeminiPart{
						Text: fmt.Sprintf("[Observation: tool apply_patch with input:\n%s\n-> returned: %s]", info.Input, string(respStr)),
					})
				} else if hasInfo && info.Arguments != "" {
					currentParts = append(currentParts, domain.GeminiPart{
						Text: fmt.Sprintf("[Observation: tool %s executed with %s -> returned: %s]", sanitizedName, info.Arguments, string(respStr)),
					})
				} else {
					currentParts = append(currentParts, domain.GeminiPart{
						Text: fmt.Sprintf("[Observation: tool %s returned: %s]", sanitizedName, string(respStr)),
					})
				}
			}

		default:
			// Fallback: don't lose custom unhandled items
			if item.Text != "" || len(item.Content) > 0 || len(item.Payload) > 0 {
				if currentRole != "" && currentRole != "user" {
					flushTurn()
				}
				currentRole = "user"
				if len(item.Content) > 0 {
					parsedParts := extractPartsFromContent(item.Content)
					if len(parsedParts) > 0 {
						currentParts = append(currentParts, parsedParts...)
					}
				} else if item.Text != "" {
					currentParts = append(currentParts, domain.GeminiPart{Text: item.Text})
				}
			}
		}
	}

	flushTurn()

	if len(contents) == 0 {
		contents = append(contents, domain.GeminiContent{
			Role:  "user",
			Parts: []domain.GeminiPart{{Text: "Hello"}},
		})
	}

	var genConfig *domain.GeminiGenerationConfig
	if thinkingLevel != "" || req.Temperature != nil || req.TopP != nil || req.MaxOutputTokens != nil {
		genConfig = &domain.GeminiGenerationConfig{
			Temperature:     req.Temperature,
			TopP:            req.TopP,
			MaxOutputTokens: req.MaxOutputTokens,
		}
		if thinkingLevel != "" {
			genConfig.ThinkingConfig = &domain.GeminiThinkingConfig{
				ThinkingLevel:   thinkingLevel,
				IncludeThoughts: true,
			}
		}
	}

	var tools []domain.GeminiTool
	if len(funcDecls) > 0 {
		tools = []domain.GeminiTool{{FunctionDeclarations: funcDecls}}
	}

	var toolConfig *domain.GeminiToolConfig
	if len(funcDecls) > 0 && req.ToolChoice != nil {
		switch tc := req.ToolChoice.(type) {
		case string:
			switch strings.ToLower(tc) {
			case "none":
				toolConfig = &domain.GeminiToolConfig{
					FunctionCallingConfig: &domain.GeminiFunctionCallingConfig{
						Mode: "NONE",
					},
				}
			case "auto":
				toolConfig = &domain.GeminiToolConfig{
					FunctionCallingConfig: &domain.GeminiFunctionCallingConfig{
						Mode: "AUTO",
					},
				}
			case "required":
				toolConfig = &domain.GeminiToolConfig{
					FunctionCallingConfig: &domain.GeminiFunctionCallingConfig{
						Mode: "ANY",
					},
				}
			}
		case map[string]any:
			if f, ok := tc["function"].(map[string]any); ok {
				if fname, ok := f["name"].(string); ok && fname != "" {
					sanitized := schema.SanitizeFunctionName(fname)
					toolConfig = &domain.GeminiToolConfig{
						FunctionCallingConfig: &domain.GeminiFunctionCallingConfig{
							Mode:                 "ANY",
							AllowedFunctionNames: []string{sanitized},
						},
					}
				}
			}
		}
	}

	contents = enforceContextBudgetOnContents(contents)

	return domain.GeminiWireBody{
		Contents:          contents,
		SystemInstruction: sysInst,
		Tools:             tools,
		ToolConfig:        toolConfig,
		GenerationConfig:  genConfig,
		SessionID:         sessionID,
	}
}
