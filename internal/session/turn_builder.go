package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"strconv"

	"sort"
	"strings"

	"comini/internal/domain"
	"comini/internal/harness"
	"comini/internal/schema"
)

func deterministicCapText(s string, maxTotalChars, headChars, tailChars int, reason string) string {
	if len(s) <= maxTotalChars {
		return s
	}
	if headChars+tailChars >= len(s) {
		return s
	}
	omitted := len(s) - headChars - tailChars
	return fmt.Sprintf("%s\n\n...[%s: %d characters omitted for context health]...\n\n%s",
		s[:headChars], reason, omitted, s[len(s)-tailChars:])
}

func generateDeterministicSignature(sessionID, callID, funcName string) string {
	hash := sha256.Sum256([]byte("sig:" + sessionID + ":" + callID + ":" + funcName))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// DeriveSessionID derives a stable session ID from Codex thread ID or first user text
// using AGY 2.0's exact FNV-1a 64-bit hash (genairequest.hashToSessionID at 0x101f7aef0).
func DeriveSessionID(parentThreadID, ownThreadID, firstUserText string) string {
	parent := strings.TrimSpace(parentThreadID)
	own := strings.TrimSpace(ownThreadID)
	var seed string
	if parent != "" && own != "" {
		seed = fmt.Sprintf("thread:%s\x00%s", parent, own)
	} else if parent != "" {
		seed = parent
	} else if own != "" {
		seed = own
	} else if firstUserText != "" {
		seed = strings.TrimSpace(firstUserText)
	}

	if seed == "" {
		seed = "comini-default-session"
	}

	var h uint64 = 0xcbf29ce484222325 // FNV-1a 64-bit offset basis
	for i := 0; i < len(seed); i++ {
		h = (h ^ uint64(seed[i])) * 0x100000001b3 // FNV-1a 64-bit prime
	}
	return strconv.FormatInt(int64(h), 10)
}

// restoreImageGenArgs rebuilds the full image_generation arguments for a replayed
// image_generation_call. Codex drops image_paths/image_name, so they are recovered from the store
// (keyed by item id, call id or saved path); without a stored record only prompt/aspect_ratio remain.
func restoreImageGenArgs(item domain.InputItem, prompt string, store *SignatureStore) map[string]any {
	args := map[string]any{"prompt": prompt}
	if store != nil {
		if stored, ok := store.GetImageCall(item.ID, item.CallID, item.SavedPath); ok {
			parsed := domain.ParseImageGenArgs(stored)
			if parsed.ImageName != "" {
				args["image_name"] = parsed.ImageName
			}
			if parsed.AspectRatio != "" {
				args["aspect_ratio"] = parsed.AspectRatio
			}
			if len(parsed.ImagePaths) > 0 {
				args["image_paths"] = parsed.ImagePaths
			}
		}
	}
	if _, ok := args["aspect_ratio"]; !ok && item.AspectRatio != "" {
		args["aspect_ratio"] = item.AspectRatio
	}
	return args
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

func normalizeValidBase64Image(mime, rawB64 string) (*domain.GeminiInlineData, bool) {
	var b strings.Builder
	b.Grow(len(rawB64))
	for i := 0; i < len(rawB64); i++ {
		c := rawB64[i]
		if c == '\r' || c == '\n' || c == ' ' || c == '\t' {
			continue
		}
		b.WriteByte(c)
	}
	clean := b.String()
	if len(clean) < 16 || len(clean)%4 != 0 {
		return nil, false
	}
	decoded, err := base64.StdEncoding.DecodeString(clean)
	if err != nil || len(decoded) < 4 {
		return nil, false
	}
	// If it starts with JPEG magic prefix (/9j/), verify it is a complete, non-truncated JPEG
	if strings.HasPrefix(clean, "/9j/") {
		if _, err := jpeg.Decode(bytes.NewReader(decoded)); err != nil {
			return nil, false
		}
	}
	canonical := base64.StdEncoding.EncodeToString(decoded)
	if mime == "" {
		mime = detectMimeType(canonical)
	}
	return &domain.GeminiInlineData{
		MimeType: mime,
		Data:     canonical,
	}, true
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
	if inData, ok := normalizeValidBase64Image(mime, data); ok {
		return inData.MimeType, inData.Data, true
	}
	return "", "", false
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
			if inData, ok := normalizeValidBase64Image(mime, rawB64); ok {
				images = append(images, inData)
				cleaned = cleaned[:idx] + fmt.Sprintf("[Attached Image: %s]", inData.MimeType) + cleaned[end:]
			} else {
				cleaned = cleaned[:idx] + "[Corrupted Base64 Image Omitted]" + cleaned[end:]
			}
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
			if inData, ok := normalizeValidBase64Image(mime, trimmed); ok {
				images = append(images, inData)
				cleaned = fmt.Sprintf("[Attached Image: %s]", inData.MimeType)
			} else {
				cleaned = "[Corrupted Base64 Image Omitted]"
			}
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
				if inData, ok := normalizeValidBase64Image(mime, val); ok {
					images = append(images, inData)
					cleanMap[k] = fmt.Sprintf("[Attached Image: %s loaded into vision encoder]", inData.MimeType)
				} else {
					cleanMap[k] = "[Corrupted Base64 Image Omitted]"
				}
				continue
			}
			if strings.Contains(val, "data:image/") {
				imgs, cleaned := extractInlineDataFromText(val)
				if len(imgs) > 0 || cleaned != val {
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
					if inData, ok := normalizeValidBase64Image(mime, data); ok {
						images = append(images, inData)
						cleanMap[k] = fmt.Sprintf("[Attached Image: %s loaded into vision encoder]", inData.MimeType)
					} else {
						cleanMap[k] = "[Corrupted Base64 Image Omitted]"
					}
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
					if len(subImgs) > 0 || subClean != elemStr {
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
			if inData, ok := normalizeValidBase64Image(mime, b64); ok {
				alreadyExtracted := false
				for _, img := range images {
					if img.Data == inData.Data {
						alreadyExtracted = true
						break
					}
				}
				if !alreadyExtracted {
					images = append(images, inData)
				}
				cleanMap["data"] = fmt.Sprintf("[Attached Image: %s loaded into vision encoder]", inData.MimeType)
			} else {
				cleanMap["data"] = "[Corrupted Base64 Image Omitted]"
			}
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
	// Antigravity 2.0 standard:
	// Never arbitrarily slice or truncate conversation parts, user prompts, model answers,
	// or tool observations. All parts are preserved 100% intact up to the native 1M token
	// context ceiling (maxCharsBudget = 3,800,000 characters).
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

func compressOversizedImage(in *domain.GeminiInlineData) *domain.GeminiInlineData {
	if in == nil || len(in.Data) <= 600000 { // ~450KB binary is already well-sized
		return in
	}
	raw, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil {
		return in
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return in
	}
	var buf bytes.Buffer
	err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 75})
	if err != nil {
		return in
	}
	newB64 := base64.StdEncoding.EncodeToString(buf.Bytes())
	if len(newB64) < len(in.Data) {
		return &domain.GeminiInlineData{
			MimeType: "image/jpeg",
			Data:     newB64,
		}
	}
	return in
}

// imageEvictionStep is the batch size used by evictStaleImages. Evicting oldest-first one image
// at a time rewrote an early part of the prompt on every new image once the window was full,
// which invalidated the whole KV prefix cache each turn (measured: cached 369,351 -> 10,084
// tokens at the 17th image). Evicting in batches rewrites history only once per step.
const imageEvictionStep = 8

func evictStaleImages(contents []domain.GeminiContent, maxKeep int) []domain.GeminiContent {
	// Total image payload budget across the request (compressOversizedImage keeps each image
	// to roughly 60-90KB of base64) so wire payloads stay well under the upstream limit.
	const maxTotalImageChars = 1600000

	type imgRef struct{ ci, pi int }
	var refs []imgRef // oldest -> newest
	var sizes []int

	for ci := range contents {
		for pi := range contents[ci].Parts {
			p := &contents[ci].Parts[pi]
			if p.InlineData == nil || len(p.InlineData.Data) == 0 {
				continue
			}
			validData, ok := normalizeValidBase64Image(p.InlineData.MimeType, p.InlineData.Data)
			if !ok {
				p.Text = "[Corrupted Base64 Image Omitted]"
				p.InlineData = nil
				continue
			}
			p.InlineData = compressOversizedImage(validData)
			refs = append(refs, imgRef{ci, pi})
			sizes = append(sizes, len(p.InlineData.Data))
		}
	}

	// Minimum number of oldest images that must go to satisfy the count and byte budgets.
	need := 0
	if len(refs) > maxKeep {
		need = len(refs) - maxKeep
	}
	kept, total := 0, 0
	for i := len(refs) - 1; i >= 0; i-- {
		if kept < maxKeep && total+sizes[i] <= maxTotalImageChars {
			kept++
			total += sizes[i]
			continue
		}
		if i+1 > need {
			need = i + 1
		}
		break
	}

	// Round up to a whole batch: the evicted set only changes once every imageEvictionStep
	// new images, so the prompt prefix stays byte-identical in between.
	if need > 0 {
		need = ((need + imageEvictionStep - 1) / imageEvictionStep) * imageEvictionStep
		if need > len(refs) {
			need = len(refs)
		}
	}

	for i := 0; i < need; i++ {
		p := &contents[refs[i].ci].Parts[refs[i].pi]
		p.Text = fmt.Sprintf("[Image: %s previously inspected and processed in earlier turn]", p.InlineData.MimeType)
		p.InlineData = nil
	}
	return contents
}

func isSyntheticThoughtText(raw string) bool {
	if strings.HasPrefix(raw, "\u200b>") || strings.HasPrefix(strings.TrimLeft(raw, " \t\r\n"), "\u200b>") {
		return true
	}
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "**Thinking**\n\n>") {
		return true
	}
	if strings.HasPrefix(trimmed, "**") && strings.Contains(trimmed, "\n\n>") {
		return true
	}
	return false
}

func isSyntheticThoughtItem(item domain.InputItem) bool {
	if strings.HasPrefix(item.ID, "msg_thought_") {
		return true
	}
	if item.Role != "" && item.Role != "assistant" {
		return false
	}
	if !strings.EqualFold(item.Phase, "commentary") {
		return false
	}
	if item.Text != "" && isSyntheticThoughtText(item.Text) {
		return true
	}
	if len(item.Content) > 0 {
		for _, p := range extractPartsFromContent(item.Content) {
			if p.Text != "" && isSyntheticThoughtText(p.Text) {
				return true
			}
		}
	}
	return false
}

func isUserConversationalContent(c domain.GeminiContent) bool {
	if c.Role != "user" {
		return false
	}
	for _, p := range c.Parts {
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
			// A genuine user instruction is not a tool observation, compaction note, or image placeholder
			if !strings.HasPrefix(trimmed, "[Observation:") &&
				!strings.HasPrefix(trimmed, "[Note:") &&
				!strings.HasPrefix(trimmed, "[Image:") {
				return true
			}
		}
	}
	return false
}

func extractPreservedUserTurns(contents []domain.GeminiContent, cutoffIndex int) []domain.GeminiContent {
	var preservedUserTurns []domain.GeminiContent
	for i := 1; i < cutoffIndex && i < len(contents); i++ {
		if isUserConversationalContent(contents[i]) {
			item := contents[i]
			var cleanedParts []domain.GeminiPart
			for _, p := range item.Parts {
				if p.InlineData != nil {
					cleanedParts = append(cleanedParts, domain.GeminiPart{
						Text: "[Image provided in earlier step]",
					})
				} else if p.Text != "" {
					txt := p.Text
					if len(txt) > 2000 {
						txt = txt[:1000] + "\n... [trimmed] ...\n" + txt[len(txt)-1000:]
					}
					cleanedParts = append(cleanedParts, domain.GeminiPart{
						Text: txt,
					})
				}
			}
			if len(cleanedParts) > 0 {
				preservedUserTurns = append(preservedUserTurns, domain.GeminiContent{
					Role:  "user",
					Parts: cleanedParts,
				})
			}
		}
	}
	if len(preservedUserTurns) > 10 {
		preservedUserTurns = preservedUserTurns[len(preservedUserTurns)-10:]
	}
	return preservedUserTurns
}

func enforceContextBudgetOnContents(contents []domain.GeminiContent, sessionID string, store *SignatureStore) []domain.GeminiContent {
	contents = capAllPartsText(contents)
	contents = sanitizeAndValidateGeminiContents(contents)

	// Measure textual/structural token weight independently of raw base64 image bytes.
	// Gemini Vision bills images at fixed tile tokens (~258-1032 tokens ≈ 4,000 chars),
	// whereas counting raw base64 length (~250,000 chars) caused cutoffIndex to jump
	// forward/backward by 40-80 turns whenever an image was added or evicted!
	calcContentSize := func(c domain.GeminiContent) int {
		total := 0
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
			if p.InlineData != nil {
				total += 4000
			}
		}
		return total
	}

	calcTotalSize := func(cts []domain.GeminiContent) int {
		total := 0
		for _, c := range cts {
			total += calcContentSize(c)
		}
		return total
	}

	// Context ceiling safety net set to 3,800,000 textual chars (~950,000 - 1,000,000 tokens).
	// Normal sessions auto-compact natively in Codex at auto_compact_token_limit = 1,000,000 tokens,
	// keeping 100% of the prefix immutable across the full 1M context window.
	const maxCharsBudget = 3800000

	// Stage 1: Enforce image budget.
	// Keep up to 16 compressed JPEG images (~1.6MB max) so earlier turns are not mutated mid-session.
	contents = evictStaleImages(contents, 16)

	// Check if this session already has a ratcheted cutoffIndex from an earlier compaction.
	lastCutoff := 0
	hasCutoff := false
	if store != nil && sessionID != "" {
		if c, ok := store.GetSessionCutoff(sessionID); ok && c > 0 {
			if c < len(contents)-5 {
				lastCutoff = c
				hasCutoff = true
			} else {
				// History shrank (e.g. Codex ran native checkpoint compaction); clear old cutoff.
				store.SetSessionCutoff(sessionID, 0)
			}
		}
	}

	// Fast path 1: If session already compacted at lastCutoff, keep cutoffIndex 100% locked
	// at lastCutoff as long as the active window fits within maxCharsBudget (800,000 chars)!
	if hasCutoff {
		first := contents[0]
		preserved := extractPreservedUserTurns(contents, lastCutoff)
		recent := contents[lastCutoff:]
		windowSize := calcContentSize(first) + calcTotalSize(preserved) + calcTotalSize(recent)
		if windowSize <= maxCharsBudget {
			newContents := make([]domain.GeminiContent, 0, 1+len(preserved)+len(recent))
			newContents = append(newContents, first)
			newContents = append(newContents, preserved...)
			newContents = append(newContents, recent...)
			return sanitizeAndValidateGeminiContents(newContents)
		}
	} else if calcTotalSize(contents) <= maxCharsBudget {
		// Fast path 2: Session has never exceeded maxCharsBudget yet.
		return sanitizeAndValidateGeminiContents(contents)
	}

	// Stage 2: Hysteresis Compaction when window exceeds maxCharsBudget (800,000 chars).
	// Drop retained tail down to 55% of budget (~440,000 chars ≈ 110,000 tokens),
	// creating 360,000 chars (~90,000 tokens ≈ 50-80 consecutive turns) of 100% frozen prefix headroom!
	retainTarget := maxCharsBudget * 55 / 100
	accumulated := 0
	rawCutoff := 1
	for i := len(contents) - 1; i >= 1; i-- {
		sz := calcContentSize(contents[i])
		if accumulated+sz > retainTarget {
			rawCutoff = i + 1
			break
		}
		accumulated += sz
	}

	cutoffIndex := (rawCutoff / 16) * 16
	if hasCutoff && cutoffIndex <= lastCutoff {
		cutoffIndex = lastCutoff + 16
	}
	if cutoffIndex <= 0 {
		cutoffIndex = 1
	}
	if cutoffIndex >= len(contents)-5 {
		cutoffIndex = len(contents) - 5
		if cutoffIndex < 1 {
			cutoffIndex = 1
		}
	}

	if store != nil && sessionID != "" {
		store.SetSessionCutoff(sessionID, cutoffIndex)
	}

	first := contents[0]
	preservedUserTurns := extractPreservedUserTurns(contents, cutoffIndex)
	recent := contents[cutoffIndex:]

	newContents := make([]domain.GeminiContent, 0, 1+len(preservedUserTurns)+len(recent))
	newContents = append(newContents, first)
	newContents = append(newContents, preservedUserTurns...)
	newContents = append(newContents, recent...)

	return sanitizeAndValidateGeminiContents(newContents)
}


// BuildGeminiWireBody converts incoming ResponsesRequest to Gemini Wire format.
func BuildGeminiWireBody(
	req *domain.ResponsesRequest,
	sessionID string,
	funcDecls []map[string]any,
	thinkingLevel string,
	store *SignatureStore,
) domain.GeminiWireBody {
	return BuildGeminiWireBodyWithItems(req, nil, sessionID, funcDecls, thinkingLevel, store)
}

// BuildGeminiWireBodyWithItems converts incoming ResponsesRequest using pre-parsed InputItems to avoid redundant JSON parsing of large payloads.
func BuildGeminiWireBodyWithItems(
	req *domain.ResponsesRequest,
	preParsedItems []domain.InputItem,
	sessionID string,
	funcDecls []map[string]any,
	thinkingLevel string,
	store *SignatureStore,
) domain.GeminiWireBody {
	var contents []domain.GeminiContent

	// 1. Parse Input Items first so we can hoist leading developer/system instructions
	// when top-level req.Instructions is omitted (Codex 0.163.0+ with use_responses_lite=false
	// sends base instructions and <skills_instructions> as leading role:"developer" items).
	items := preParsedItems
	if len(items) == 0 && len(req.Input) > 0 {
		_ = json.Unmarshal(req.Input, &items)
	}

	normalizeType := func(t string) string {
		t = strings.ToLower(strings.TrimSpace(t))
		t = strings.ReplaceAll(t, "_", "")
		t = strings.ReplaceAll(t, "-", "")
		return t
	}

	extractItemTextOnly := func(it domain.InputItem) (string, bool) {
		if len(it.Content) > 0 {
			parts := extractPartsFromContent(it.Content)
			if len(parts) == 0 {
				return "", false
			}
			var texts []string
			for _, p := range parts {
				if p.InlineData != nil {
					return "", false
				}
				if strings.TrimSpace(p.Text) != "" {
					texts = append(texts, p.Text)
				}
			}
			return strings.Join(texts, "\n\n"), len(texts) > 0
		}
		if strings.TrimSpace(it.Text) != "" {
			imgs, cleaned := extractInlineDataFromText(it.Text)
			if len(imgs) > 0 {
				return "", false
			}
			cleanTxt := harness.SanitizeMessageText(cleaned)
			return cleanTxt, strings.TrimSpace(cleanTxt) != ""
		}
		return "", false
	}

	effectiveInstructions := req.Instructions
	if effectiveInstructions == nil || strings.TrimSpace(*effectiveInstructions) == "" {
		var devTexts []string
		hoistCount := 0
		for hoistCount < len(items) {
			it := items[hoistCount]
			norm := normalizeType(it.Type)
			role := strings.ToLower(strings.TrimSpace(it.Role))
			isDev := norm == "system" || norm == "developer" ||
				((norm == "message" || norm == "usermessage" || norm == "text" || norm == "inputtext" || norm == "") &&
					(role == "developer" || role == "system"))
			if !isDev {
				break
			}
			txt, ok := extractItemTextOnly(it)
			if !ok {
				break
			}
			devTexts = append(devTexts, txt)
			hoistCount++
		}
		// Only hoist leading developer/system items if at least one item remains for Contents,
		// so we never empty Contents into a synthetic "Hello" turn.
		if hoistCount > 0 && hoistCount < len(items) {
			joined := strings.Join(devTexts, "\n\n")
			effectiveInstructions = &joined
			items = items[hoistCount:]
		}
	}

	// 2. Collect tool names and sanitize System Instruction
	toolNames := make([]string, 0, len(funcDecls))
	for _, decl := range funcDecls {
		if name, ok := decl["name"].(string); ok && name != "" {
			toolNames = append(toolNames, name)
		}
	}

	sanitizedSys := harness.SanitizeSystemInstruction(effectiveInstructions, toolNames)
	var sysInst *domain.GeminiSystemInstruction
	if sanitizedSys != nil {
		txt := *sanitizedSys
		if len(txt) > 2000000 {
			txt = txt[:2000000]
		}
		sysInst = &domain.GeminiSystemInstruction{
			Role:  "user",
			Parts: []domain.GeminiPart{{Text: txt}},
		}
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
			if it.Server != "" {
				server := strings.TrimPrefix(it.Server, "mcp__")
				name = fmt.Sprintf("mcp__%s__%s", server, name)
			} else if it.Namespace != "" {
				if strings.HasPrefix(it.Namespace, "mcp__") {
					name = fmt.Sprintf("%s__%s", it.Namespace, name)
				} else if it.Namespace == "collaboration" {
					name = fmt.Sprintf("%s__%s", it.Namespace, name)
				} else {
					name = fmt.Sprintf("mcp__%s__%s", it.Namespace, name)
				}
			}
			name = strings.ReplaceAll(name, ":", "__")
			if name == "" && norm == "localshellcall" {
				name = "exec_command"
			}
			if name == "" && norm == "imagegenerationcall" {
				name = "image_generation"
			}
			if name == "" && norm == "toolsearchcall" {
				name = "tool_search"
			}
			args := it.Arguments
			if args == "" {
				args = it.Input
			}
			if args == "" && it.Command != "" {
				args = fmt.Sprintf("{\"cmd\":%q}", it.Command)
			}
			if args == "" && norm == "imagegenerationcall" {
				p := it.RevisedPrompt
				if p == "" {
					p = it.Prompt
				}
				if p == "" {
					p = it.Text
				}
				m := restoreImageGenArgs(it, p, store)
				b, _ := json.Marshal(m)
				args = string(b)
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
						sort.Strings(fileNames) // Deterministic ordering guarantees prefix cache hit
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
			if isSyntheticThoughtItem(item) {
				continue
			}
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
			if isSyntheticThoughtItem(item) {
				continue
			}
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
				cappedOut := deterministicCapText(outText, 16000, 10000, 3000, "terminal stdout truncated")
				currentParts = append(currentParts, domain.GeminiPart{Text: fmt.Sprintf("[Observation: command %s executed -> output: %s]", item.Command, cappedOut)})
			}

		case "imagegenerationcall":
			prompt := item.RevisedPrompt
			if prompt == "" {
				prompt = item.Prompt
			}
			if prompt == "" {
				prompt = item.Text
			}
			argsMap := restoreImageGenArgs(item, prompt, store)
			argsJSON, _ := json.Marshal(argsMap)

			if currentRole != "" && currentRole != "model" {
				flushTurn()
			}
			currentRole = "model"
			currentParts = append(currentParts, domain.GeminiPart{
				Text: fmt.Sprintf("[Action: called image_generation with %s]", string(argsJSON)),
			})
			flushTurn()

			currentRole = "user"
			var obsText string
			if item.Failure != "" || item.Status == "incomplete" {
				errText := item.Failure
				if errText == "" {
					errText = "generation failed"
				}
				obsText = fmt.Sprintf("[Observation: tool image_generation failed with error: %s]", errText)
			} else if item.SavedPath != "" {
				obsText = fmt.Sprintf("[Observation: tool image_generation succeeded -> saved to: %s. To display this image to the user, include Markdown embed ![%s](%s) in your message]", item.SavedPath, prompt, item.SavedPath)
			} else {
				obsText = fmt.Sprintf("[Observation: tool image_generation succeeded -> prompt: %s]", prompt)
			}
			currentParts = append(currentParts, domain.GeminiPart{
				Text: obsText,
			})

		case "mcptoolcall", "dynamictoolcall", "collabagenttoolcall", "functioncall", "websearchcall", "toolsearchcall", "customtoolcall", "localshellcall":
			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}
			if callID != "" {
				untrackedCalls[callID] = true
			}

			if currentRole != "" && currentRole != "model" {
				flushTurn()
			}
			currentRole = "model"

			funcName := item.Name
			if funcName == "" && item.Tool != "" {
				funcName = item.Tool
			}
			if item.Server != "" {
				server := strings.TrimPrefix(item.Server, "mcp__")
				funcName = fmt.Sprintf("mcp__%s__%s", server, funcName)
			} else if item.Namespace != "" {
				if strings.HasPrefix(item.Namespace, "mcp__") {
					funcName = fmt.Sprintf("%s__%s", item.Namespace, funcName)
				} else if item.Namespace == "collaboration" {
					funcName = fmt.Sprintf("%s__%s", item.Namespace, funcName)
				} else {
					funcName = fmt.Sprintf("mcp__%s__%s", item.Namespace, funcName)
				}
			}
			if funcName == "" && normType == "localshellcall" {
				funcName = "exec_command"
			}
			if funcName == "" && normType == "toolsearchcall" {
				funcName = "tool_search"
			}
			if funcName == "" && callID != "" {
				if info, ok := callInfoMap[callID]; ok && info.Name != "" {
					funcName = info.Name
				}
			}
			if funcName == "" {
				funcName = callID
			}
			funcName = strings.ReplaceAll(funcName, ":", "__")
			sanitizedName := schema.SanitizeFunctionName(funcName)

			args := item.Arguments
			if args == "" {
				args = item.Input
			}
			if args == "" && item.Command != "" {
				args = fmt.Sprintf("{\"cmd\":%q}", item.Command)
			}
			if args == "" && callID != "" {
				if info, ok := callInfoMap[callID]; ok && info.Arguments != "" {
					args = info.Arguments
				}
			}
			if args == "" {
				args = "{}"
			}

			// Build canonical argsMap
			var argsMap map[string]any
			trimmedArgs := strings.TrimSpace(args)
			if strings.HasPrefix(trimmedArgs, "{") {
				_ = json.Unmarshal([]byte(trimmedArgs), &argsMap)
			}
			if argsMap == nil {
				if item.Input != "" {
					argsMap = map[string]any{"input": item.Input}
				} else if trimmedArgs != "" && trimmedArgs != "{}" {
					argsMap = map[string]any{"input": trimmedArgs}
				} else {
					argsMap = map[string]any{}
				}
			}

			// Retrieve thoughtSignature from store
			var sig string
			if store != nil && callID != "" {
				s, _, ok := store.Get(callID)
				if ok && s != "" {
					sig = s
				}
			}
			if sig == "" && store != nil {
				if s, ok := store.GetSessionCall(sessionID, sanitizedName, argsMap); ok && s != "" {
					sig = s
				} else if s, ok := store.FindAnyCall(sanitizedName, argsMap); ok && s != "" {
					sig = s
					if sessionID != "" {
						store.SetSessionCall(sessionID, sanitizedName, argsMap, sig)
					}
				}
			}

			if sig != "" {
				// NATIVE Gemini FunctionCall with valid ThoughtSignature
				currentParts = append(currentParts, domain.GeminiPart{
					FunctionCall: &domain.GeminiFunctionCall{
						Name: sanitizedName,
						Args: argsMap,
					},
					ThoughtSignature: sig,
				})
			} else {
				// Fallback when thoughtSignature is genuinely absent:
				// Format args as valid JSON so Gemini sees valid JSON in prompt
				argsJSON, _ := json.Marshal(argsMap)
				currentParts = append(currentParts, domain.GeminiPart{
					Text: fmt.Sprintf("[Action: called %s with %s]", sanitizedName, string(argsJSON)),
				})
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
			if funcName == "" && item.Tool != "" {
				funcName = item.Tool
			}
			if item.Server != "" {
				server := strings.TrimPrefix(item.Server, "mcp__")
				funcName = fmt.Sprintf("mcp__%s__%s", server, funcName)
			} else if item.Namespace != "" {
				if strings.HasPrefix(item.Namespace, "mcp__") {
					funcName = fmt.Sprintf("%s__%s", item.Namespace, funcName)
				} else if item.Namespace == "collaboration" {
					funcName = fmt.Sprintf("%s__%s", item.Namespace, funcName)
				} else {
					funcName = fmt.Sprintf("mcp__%s__%s", item.Namespace, funcName)
				}
			}
			if funcName == "" && normType == "toolsearchoutput" {
				funcName = "tool_search"
			}
			if funcName == "" {
				funcName = callID
			}
			funcName = strings.ReplaceAll(funcName, ":", "__")
			sanitizedName := schema.SanitizeFunctionName(funcName)

			extractedImages, cleanResp := extractImagesAndCleanOutput(item.Output)

			// 1. Prepare response object for FunctionResponse
			respMap := cleanResp
			if respMap == nil {
				respMap = map[string]any{}
			}
			if len(item.Output) == 0 && len(item.Tools) > 0 {
				var discoveredNames []string
				for _, t := range item.Tools {
					if t.Name != "" {
						discoveredNames = append(discoveredNames, t.Name)
					}
				}
				respMap = map[string]any{
					"status":           "ok",
					"discovered_tools": discoveredNames,
				}
			}

			// 2. ALWAYS append FunctionResponse matching the model's FunctionCall
			currentParts = append(currentParts, domain.GeminiPart{
				FunctionResponse: &domain.GeminiFunctionResponse{
					Name:     sanitizedName,
					Response: respMap,
				},
			})

			// 3. Attach native Vision inlineData parts alongside FunctionResponse (if any)
			for _, img := range extractedImages {
				currentParts = append(currentParts, domain.GeminiPart{InlineData: img})
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
						Mode: "VALIDATED",
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

	contents = enforceContextBudgetOnContents(contents, sessionID, store)

	return domain.GeminiWireBody{
		Contents:          contents,
		SystemInstruction: sysInst,
		Tools:             tools,
		ToolConfig:        toolConfig,
		GenerationConfig:  genConfig,
		SessionID:         sessionID,
	}
}
