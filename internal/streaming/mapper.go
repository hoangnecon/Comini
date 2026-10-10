package streaming

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"comini/internal/domain"
	"comini/internal/session"
)

func randomID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b))
}

func formatSSEWithSeq(eventType string, data map[string]any, seq int) string {
	data["type"] = eventType
	data["sequence_number"] = seq
	b, _ := json.Marshal(data)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(b))
}

// MapGeminiStreamToResponses parses raw Gemini SSE stream and writes Responses API events to w.
func MapGeminiStreamToResponses(
	r io.Reader,
	w io.Writer,
	flusher func(),
	store *session.SignatureStore,
	responseID string,
	modelName string,
	toolMetaMap map[string]domain.ToolMeta,
	sessionID string,
) error {
	if responseID == "" {
		responseID = randomID("resp")
	}
	if modelName == "" {
		modelName = "deepseek-flash"
	}

	createdAt := time.Now().Unix()
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 20*1024*1024)
	seq := 0
	outputIndex := 0

	var currentReasoningID string
	var reasoningIndex int
	var accumulatedReasoning strings.Builder

	var currentMessageID string
	var messageIndex int
	var accumulatedMessage strings.Builder

	var pendingThoughtSig string
	var completedOutputItems []map[string]any

	var inputTokens, outputTokens, reasoningTokens int

	flushSSE := func() {
		if flusher != nil {
			flusher()
		}
	}

	var writeErr error
	emitSSE := func(eventType string, data map[string]any) {
		if writeErr != nil {
			return
		}
		currentSeq := seq
		seq++
		_, writeErr = io.WriteString(w, formatSSEWithSeq(eventType, data, currentSeq))
	}

	closeReasoning := func() {
		if currentReasoningID == "" {
			return
		}
		fullReasoning := accumulatedReasoning.String()

		// 1. Done event for reasoning summary text
		emitSSE("response.reasoning_summary_text.done", map[string]any{
			"output_index":  reasoningIndex,
			"item_id":       currentReasoningID,
			"summary_index": 0,
			"text":          fullReasoning,
		})

		// 2. Done event for reasoning summary part
		emitSSE("response.reasoning_summary_part.done", map[string]any{
			"output_index":  reasoningIndex,
			"item_id":       currentReasoningID,
			"summary_index": 0,
			"part": map[string]any{
				"type": "summary_text",
				"text": fullReasoning,
			},
		})

		reasoningItem := map[string]any{
			"id":      currentReasoningID,
			"type":    "reasoning",
			"status":  "completed",
			"summary": []any{
				map[string]any{
					"type": "summary_text",
					"text": fullReasoning,
				},
			},
			"content": []any{
				map[string]any{
					"type": "reasoning_text",
					"text": fullReasoning,
				},
			},
		}
		completedOutputItems = append(completedOutputItems, reasoningItem)

		emitSSE("response.output_item.done", map[string]any{
			"output_index": reasoningIndex,
			"item":         reasoningItem,
		})

		currentReasoningID = ""
		accumulatedReasoning.Reset()
		flushSSE()
	}

	closeMessage := func() {
		if currentMessageID == "" {
			return
		}
		fullMsg := accumulatedMessage.String()

		emitSSE("response.output_text.done", map[string]any{
			"output_index":  messageIndex,
			"item_id":       currentMessageID,
			"content_index": 0,
			"text":          fullMsg,
		})

		emitSSE("response.content_part.done", map[string]any{
			"output_index":  messageIndex,
			"item_id":       currentMessageID,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        fullMsg,
				"annotations": []any{},
			},
		})

		msgItem := map[string]any{
			"id":     currentMessageID,
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []any{
				map[string]any{
					"type":        "output_text",
					"text":        fullMsg,
					"annotations": []any{},
				},
			},
		}
		completedOutputItems = append(completedOutputItems, msgItem)

		emitSSE("response.output_item.done", map[string]any{
			"output_index": messageIndex,
			"item":         msgItem,
		})

		currentMessageID = ""
		accumulatedMessage.Reset()
		flushSSE()
	}

	// 1. Emit initial response.created
	emitSSE("response.created", map[string]any{
		"response": map[string]any{
			"id":         responseID,
			"object":     "response",
			"created_at": createdAt,
			"status":     "in_progress",
			"model":      modelName,
			"output":     []any{},
			"usage":      nil,
		},
	})
	flushSSE()

	for scanner.Scan() {
		if writeErr != nil {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		payloadStr := strings.TrimSpace(line[5:])
		if payloadStr == "" || payloadStr == "[DONE]" {
			continue
		}

		var root domain.CCARootChunk
		if err := json.Unmarshal([]byte(payloadStr), &root); err != nil {
			continue
		}

		// Extract candidates and usage
		var candidates []domain.GeminiCandidate
		if root.Response != nil && len(root.Response.Candidates) > 0 {
			candidates = root.Response.Candidates
			if root.Response.UsageMetadata != nil {
				inputTokens = root.Response.UsageMetadata.PromptTokenCount
				outputTokens = root.Response.UsageMetadata.CandidatesTokenCount
				reasoningTokens = root.Response.UsageMetadata.ThoughtsTokenCount
			}
		} else if len(root.Candidates) > 0 {
			candidates = root.Candidates
			if root.UsageMetadata != nil {
				inputTokens = root.UsageMetadata.PromptTokenCount
				outputTokens = root.UsageMetadata.CandidatesTokenCount
				reasoningTokens = root.UsageMetadata.ThoughtsTokenCount
			}
		}

		if len(candidates) == 0 && root.Response != nil && root.Response.UsageMetadata != nil {
			inputTokens = root.Response.UsageMetadata.PromptTokenCount
			outputTokens = root.Response.UsageMetadata.CandidatesTokenCount
			reasoningTokens = root.Response.UsageMetadata.ThoughtsTokenCount
		}

		for _, cand := range candidates {
			for _, part := range cand.Content.Parts {
				// Record thought signature
				if part.ThoughtSignature != "" {
					pendingThoughtSig = part.ThoughtSignature
				}

				// A. Thinking / Reasoning Part
				if part.Thought && len(part.Text) > 0 {
					if currentReasoningID == "" {
						currentReasoningID = randomID("rs")
						reasoningIndex = outputIndex
						outputIndex++
						accumulatedReasoning.Reset()

						emitSSE("response.output_item.added", map[string]any{
							"output_index": reasoningIndex,
							"item": map[string]any{
								"id":      currentReasoningID,
								"type":    "reasoning",
								"status":  "in_progress",
								"summary": []any{},
							},
						})

						emitSSE("response.reasoning_summary_part.added", map[string]any{
							"output_index":  reasoningIndex,
							"item_id":       currentReasoningID,
							"summary_index": 0,
							"part": map[string]any{
								"type": "summary_text",
								"text": "",
							},
						})
					}

					accumulatedReasoning.WriteString(part.Text)

					// Stream full multiline thought to reasoning_summary_text.delta (triggers Accordion in Codex Desktop)
					emitSSE("response.reasoning_summary_text.delta", map[string]any{
						"output_index":  reasoningIndex,
						"item_id":       currentReasoningID,
						"summary_index": 0,
						"delta":         part.Text,
					})

					flushSSE()
					continue
				}

				// Transition out of reasoning
				if currentReasoningID != "" && !part.Thought {
					closeReasoning()
				}

				// B. Standard Text Content Part
				if len(part.Text) > 0 {
					// Intercept plain text action format and promote to structured FunctionCall
					if fnName, fnArgs, ok := extractActionFromText(part.Text); ok {
						part.FunctionCall = &domain.GeminiFunctionCall{
							Name: fnName,
							Args: fnArgs,
						}
						part.Text = ""
					}
				}

				if len(part.Text) > 0 {
					if currentMessageID == "" {
						currentMessageID = randomID("msg")
						messageIndex = outputIndex
						outputIndex++
						accumulatedMessage.Reset()

						emitSSE("response.output_item.added", map[string]any{
							"output_index": messageIndex,
							"item": map[string]any{
								"id":      currentMessageID,
								"type":    "message",
								"role":    "assistant",
								"status":  "in_progress",
								"content": []any{},
							},
						})

						emitSSE("response.content_part.added", map[string]any{
							"output_index":  messageIndex,
							"item_id":       currentMessageID,
							"content_index": 0,
							"part": map[string]any{
								"type":        "output_text",
								"text":        "",
								"annotations": []any{},
							},
						})
					}

					accumulatedMessage.WriteString(part.Text)

					emitSSE("response.output_text.delta", map[string]any{
						"output_index":  messageIndex,
						"item_id":       currentMessageID,
						"content_index": 0,
						"delta":         part.Text,
					})

					flushSSE()
					continue
				}

				// C. Function / Tool Call Part
				if part.FunctionCall != nil {
					closeReasoning()
					closeMessage()

					callID := randomID("call")
					fcIndex := outputIndex
					outputIndex++

					geminiName := part.FunctionCall.Name
					meta, hasMeta := toolMetaMap[geminiName]
					if !hasMeta {
						if m, ok := toolMetaMap[strings.ReplaceAll(geminiName, ":", "__")]; ok {
							meta, hasMeta = m, true
						} else if m, ok := toolMetaMap[strings.ReplaceAll(geminiName, "__", ":")]; ok {
							meta, hasMeta = m, true
						} else if strings.HasPrefix(geminiName, "mcp__") {
							if m, ok := toolMetaMap[strings.TrimPrefix(geminiName, "mcp__")]; ok {
								meta, hasMeta = m, true
							}
						} else if m, ok := toolMetaMap["mcp__"+geminiName]; ok {
							meta, hasMeta = m, true
						} else if idx := strings.LastIndex(geminiName, ":"); idx != -1 {
							if m, ok := toolMetaMap[geminiName[idx+1:]]; ok {
								meta, hasMeta = m, true
							}
						} else if idx := strings.LastIndex(geminiName, "__"); idx != -1 {
							if m, ok := toolMetaMap[geminiName[idx+2:]]; ok {
								meta, hasMeta = m, true
							}
						}
					}

					origName := geminiName
					namespace := ""
					if hasMeta {
						if meta.OriginalName != "" {
							origName = meta.OriginalName
						}
						namespace = meta.Namespace
					}

					sigToStore := pendingThoughtSig
					if part.ThoughtSignature != "" {
						sigToStore = part.ThoughtSignature
					}

					if hasMeta && meta.IsCustom {
						// Freeform custom tool (apply_patch)
						var inputStr string
						if rawInput, ok := part.FunctionCall.Args["input"].(string); ok {
							inputStr = rawInput
						} else {
							b, _ := json.Marshal(part.FunctionCall.Args)
							inputStr = string(b)
						}

						customItemID := randomID("ctc")
						if store != nil {
							store.Set(callID, sigToStore, geminiName)
							store.Set(customItemID, sigToStore, geminiName)
							if sessionID != "" {
								store.SetSessionCall(sessionID, origName, map[string]any{"input": inputStr}, sigToStore)
								store.SetSessionCall(sessionID, geminiName, map[string]any{"input": inputStr}, sigToStore)
							}
						}

						customItem := map[string]any{
							"id":      customItemID,
							"call_id": callID,
							"type":    "custom_tool_call",
							"name":    origName,
							"input":   inputStr,
							"status":  "completed",
						}
						if namespace != "" {
							customItem["namespace"] = namespace
						}
						completedOutputItems = append(completedOutputItems, customItem)

						inProgCustom := map[string]any{
							"id":      customItemID,
							"call_id": callID,
							"type":    "custom_tool_call",
							"name":    origName,
							"input":   "",
							"status":  "in_progress",
						}
						if namespace != "" {
							inProgCustom["namespace"] = namespace
						}

						emitSSE("response.output_item.added", map[string]any{
							"output_index": fcIndex,
							"item":         inProgCustom,
						})

						emitSSE("response.custom_tool_call_input.delta", map[string]any{
							"output_index": fcIndex,
							"item_id":      customItemID,
							"call_id":      callID,
							"delta":        inputStr,
						})

						doneCustom := map[string]any{
							"output_index": fcIndex,
							"item_id":      customItemID,
							"call_id":      callID,
							"name":         origName,
							"input":        inputStr,
						}
						if namespace != "" {
							doneCustom["namespace"] = namespace
						}
						emitSSE("response.custom_tool_call_input.done", doneCustom)

						emitSSE("response.output_item.done", map[string]any{
							"output_index": fcIndex,
							"item":         customItem,
						})

					} else if (hasMeta && meta.IsToolSearch) || geminiName == "tool_search" || origName == "tool_search" {
						// Dedicated Tool Search Call (client discovery expects arguments to be a JSON Object struct SearchToolCallParams)
						searchParams := make(map[string]any)
						if part.FunctionCall.Args != nil {
							for k, v := range part.FunctionCall.Args {
								searchParams[k] = v
							}
						}
						if _, ok := searchParams["query"]; !ok {
							if q, ok := searchParams["q"].(string); ok {
								searchParams["query"] = q
							}
						}
						if q, ok := searchParams["query"].(string); ok {
							cleanQ := strings.TrimPrefix(q, "mcp__")
							cleanQ = strings.ReplaceAll(cleanQ, "__", " ")
							cleanQ = strings.ReplaceAll(cleanQ, ":", " ")
							cleanQ = strings.TrimSpace(cleanQ)
							if cleanQ != "" {
								searchParams["query"] = cleanQ
							}
						}

						tscItemID := randomID("tsc")
						if store != nil {
							store.Set(callID, sigToStore, geminiName)
							store.Set(tscItemID, sigToStore, geminiName)
							if sessionID != "" {
								store.SetSessionCall(sessionID, origName, searchParams, sigToStore)
								store.SetSessionCall(sessionID, geminiName, searchParams, sigToStore)
							}
						}

						tscItem := map[string]any{
							"id":        tscItemID,
							"call_id":   callID,
							"type":      "tool_search_call",
							"status":    "completed",
							"execution": "client",
							"arguments": searchParams,
						}
						completedOutputItems = append(completedOutputItems, tscItem)

						inProgItem := map[string]any{
							"id":        tscItemID,
							"call_id":   callID,
							"type":      "tool_search_call",
							"status":    "in_progress",
							"execution": "client",
							"arguments": searchParams,
						}

						emitSSE("response.output_item.added", map[string]any{
							"output_index": fcIndex,
							"item":         inProgItem,
						})

						emitSSE("response.output_item.done", map[string]any{
							"output_index": fcIndex,
							"item":         tscItem,
						})

					} else {
						// Standard function call
						argsJSON, _ := json.Marshal(part.FunctionCall.Args)
						argsStr := string(argsJSON)
						if argsStr == "null" || argsStr == "" {
							argsStr = "{}"
						}

						targetName := origName
						targetNamespace := namespace
						if hasMeta {
							if meta.Namespace != "" {
								targetNamespace = meta.Namespace
								targetName = meta.OriginalName
							} else {
								targetName = meta.OriginalName
							}
						} else {
							if strings.HasPrefix(origName, "mcp__") {
								trimmed := strings.TrimPrefix(origName, "mcp__")
								parts := strings.SplitN(trimmed, "__", 2)
								if len(parts) == 2 {
									targetNamespace = "mcp__" + parts[0]
									targetName = parts[1]
								}
							} else if idx := strings.Index(origName, ":"); idx != -1 {
								targetNamespace = origName[:idx]
								targetName = origName[idx+1:]
							}
						}

						fcItemID := randomID("fc")
						if store != nil {
							store.Set(callID, sigToStore, geminiName)
							store.Set(fcItemID, sigToStore, geminiName)
							if sessionID != "" {
								store.SetSessionCall(sessionID, targetName, part.FunctionCall.Args, sigToStore)
								store.SetSessionCall(sessionID, origName, part.FunctionCall.Args, sigToStore)
								store.SetSessionCall(sessionID, geminiName, part.FunctionCall.Args, sigToStore)
							}
						}

						fcItem := map[string]any{
							"id":        fcItemID,
							"call_id":   callID,
							"type":      "function_call",
							"name":      targetName,
							"arguments": argsStr,
							"status":    "completed",
						}
						if targetNamespace != "" {
							fcItem["namespace"] = targetNamespace
						}
						completedOutputItems = append(completedOutputItems, fcItem)

						inProgItem := map[string]any{
							"id":        fcItemID,
							"call_id":   callID,
							"type":      "function_call",
							"name":      targetName,
							"arguments": "",
							"status":    "in_progress",
						}
						if targetNamespace != "" {
							inProgItem["namespace"] = targetNamespace
						}

						emitSSE("response.output_item.added", map[string]any{
							"output_index": fcIndex,
							"item":         inProgItem,
						})

						emitSSE("response.function_call_arguments.delta", map[string]any{
							"output_index": fcIndex,
							"item_id":      fcItemID,
							"call_id":      callID,
							"delta":        argsStr,
						})

						doneFunc := map[string]any{
							"output_index": fcIndex,
							"item_id":      fcItemID,
							"call_id":      callID,
							"name":         targetName,
							"arguments":    argsStr,
						}
						if targetNamespace != "" {
							doneFunc["namespace"] = targetNamespace
						}
						emitSSE("response.function_call_arguments.done", doneFunc)

						emitSSE("response.output_item.done", map[string]any{
							"output_index": fcIndex,
							"item":         fcItem,
						})
					}

					flushSSE()
				}
			}
		}
	}

	// Close open items
	closeReasoning()
	closeMessage()

	if inputTokens == 0 {
		inputTokens = 1
	}
	if outputTokens == 0 {
		outputTokens = 1
	}

	// Terminal event: response.completed with strict Async-OpenAI / Codex deserialization support
	emitSSE("response.completed", map[string]any{
		"response": map[string]any{
			"id":         responseID,
			"object":     "response",
			"created_at": createdAt,
			"status":     "completed",
			"model":      modelName,
			"output":     completedOutputItems,
			"usage": map[string]any{
				"input_tokens":  inputTokens,
				"output_tokens": outputTokens,
				"total_tokens":  inputTokens + outputTokens,
				"input_tokens_details": map[string]any{
					"cached_tokens": 0,
				},
				"output_tokens_details": map[string]any{
					"reasoning_tokens": reasoningTokens,
				},
			},
		},
	})

	// Finalize SSE stream
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flushSSE()

	return scanner.Err()
}

func extractActionFromText(text string) (string, map[string]any, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "[Action: called ") {
		return "", nil, false
	}
	rest := strings.TrimPrefix(trimmed, "[Action: called ")
	rest = strings.TrimSuffix(rest, "]")

	// Format: "<name> with <args>" or "<name> with args: <args>"
	withIdx := strings.Index(rest, " with ")
	if withIdx == -1 {
		return "", nil, false
	}
	funcName := strings.TrimSpace(rest[:withIdx])
	argsPart := strings.TrimSpace(rest[withIdx+len(" with "):])
	argsPart = strings.TrimPrefix(argsPart, "args:")
	argsPart = strings.TrimSpace(argsPart)

	var argsMap map[string]any
	if err := json.Unmarshal([]byte(argsPart), &argsMap); err == nil && argsMap != nil {
		return funcName, argsMap, true
	}
	return "", nil, false
}

