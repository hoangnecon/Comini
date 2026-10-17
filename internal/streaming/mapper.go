package streaming

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"comini/internal/domain"
	"comini/internal/session"
)

type StreamOptions struct {
	Context    context.Context
	ImageGenFn func(ctx context.Context, prompt string, aspectRatio string) (savedPath string, dataURL string, err error)
	ContinueFn func(ctx context.Context, modelPart domain.GeminiPart, observationText string) (io.ReadCloser, error)
}

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
	opts ...StreamOptions,
) error {
	var streamOpts StreamOptions
	if len(opts) > 0 {
		streamOpts = opts[0]
	}
	if responseID == "" {
		responseID = randomID("resp")
	}
	if modelName == "" {
		modelName = "gemini-3.8-flash"
	}

	createdAt := time.Now().Unix()
	currentReader := r
	defer func() {
		if rc, ok := currentReader.(io.Closer); ok {
			_ = rc.Close()
		}
	}()
	scanner := bufio.NewScanner(currentReader)
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
	msgFilter := newStreamFilter()

	var pendingThoughtSig string
	var completedOutputItems []map[string]any

	type generatedImageRecord struct {
		Prompt    string
		SavedPath string
	}
	var allGeneratedImages []generatedImageRecord
	hasEmittedMessage := false
	loopCount := 0

	var inputTokens, outputTokens, reasoningTokens, cachedTokens int

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
			rem := msgFilter.flush()
			if len(rem) == 0 {
				return
			}
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

			accumulatedMessage.WriteString(rem)
			emitSSE("response.output_text.delta", map[string]any{
				"output_index":  messageIndex,
				"item_id":       currentMessageID,
				"content_index": 0,
				"delta":         rem,
			})
			flushSSE()
		} else {
			rem := msgFilter.flush()
			if len(rem) > 0 {
				accumulatedMessage.WriteString(rem)
				emitSSE("response.output_text.delta", map[string]any{
					"output_index":  messageIndex,
					"item_id":       currentMessageID,
					"content_index": 0,
					"delta":         rem,
				})
			}
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
		msgFilter.reset()
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
				if root.Response.UsageMetadata.CachedContentTokenCount > 0 {
					cachedTokens = root.Response.UsageMetadata.CachedContentTokenCount
				}
			}
		} else if len(root.Candidates) > 0 {
			candidates = root.Candidates
			if root.UsageMetadata != nil {
				inputTokens = root.UsageMetadata.PromptTokenCount
				outputTokens = root.UsageMetadata.CandidatesTokenCount
				reasoningTokens = root.UsageMetadata.ThoughtsTokenCount
				if root.UsageMetadata.CachedContentTokenCount > 0 {
					cachedTokens = root.UsageMetadata.CachedContentTokenCount
				}
			}
		}

		if len(candidates) == 0 && root.Response != nil && root.Response.UsageMetadata != nil {
			inputTokens = root.Response.UsageMetadata.PromptTokenCount
			outputTokens = root.Response.UsageMetadata.CandidatesTokenCount
			reasoningTokens = root.Response.UsageMetadata.ThoughtsTokenCount
			if root.Response.UsageMetadata.CachedContentTokenCount > 0 {
				cachedTokens = root.Response.UsageMetadata.CachedContentTokenCount
			}
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
				textDelta, actionCall := msgFilter.feed(part.Text)
				if actionCall != nil {
					part.FunctionCall = actionCall
				}
				if part.FunctionCall == nil && len(textDelta) > 0 {
					// Intercept plain text action format and promote to structured FunctionCall
					if fnName, fnArgs, ok := extractActionFromText(textDelta); ok {
						part.FunctionCall = &domain.GeminiFunctionCall{
							Name: fnName,
							Args: fnArgs,
						}
						textDelta = ""
					} else if fnName, fnArgs, ok := extractImageGenFromText(textDelta); ok {
						part.FunctionCall = &domain.GeminiFunctionCall{
							Name: fnName,
							Args: fnArgs,
						}
						textDelta = ""
					}
				}

				if len(textDelta) > 0 {
					hasEmittedMessage = true
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

					accumulatedMessage.WriteString(textDelta)

					emitSSE("response.output_text.delta", map[string]any{
						"output_index":  messageIndex,
						"item_id":       currentMessageID,
						"content_index": 0,
						"delta":         textDelta,
					})

					flushSSE()
					if part.FunctionCall == nil {
						continue
					}
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

					} else if (hasMeta && meta.IsImageGen) || geminiName == "image_generation" || origName == "image_generation" || origName == "generate_image" {
						// Dedicated Image Generation Call (executed natively via Gemini Imagen)
						prompt := ""
						if p, ok := part.FunctionCall.Args["prompt"].(string); ok {
							prompt = p
						} else if q, ok := part.FunctionCall.Args["description"].(string); ok {
							prompt = q
						} else if q, ok := part.FunctionCall.Args["query"].(string); ok {
							prompt = q
						}
						aspectRatio := ""
						if ar, ok := part.FunctionCall.Args["aspect_ratio"].(string); ok {
							aspectRatio = ar
						}

						igcItemID := randomID("igc")
						if store != nil {
							store.Set(callID, sigToStore, geminiName)
							store.Set(igcItemID, sigToStore, geminiName)
							if sessionID != "" {
								store.SetSessionCall(sessionID, "image_generation", part.FunctionCall.Args, sigToStore)
								store.SetSessionCall(sessionID, origName, part.FunctionCall.Args, sigToStore)
								store.SetSessionCall(sessionID, geminiName, part.FunctionCall.Args, sigToStore)
							}
						}

						inProgItem := map[string]any{
							"id":        igcItemID,
							"call_id":   callID,
							"type":      "image_generation_call",
							"status":    "in_progress",
						}

						emitSSE("response.output_item.added", map[string]any{
							"output_index": fcIndex,
							"item":         inProgItem,
						})
						flushSSE()

						var savedPath, dataURL string
						var genErr error
						if streamOpts.ImageGenFn != nil {
							callCtx := streamOpts.Context
							if callCtx == nil {
								callCtx = context.Background()
							}
							savedPath, dataURL, genErr = streamOpts.ImageGenFn(callCtx, prompt, aspectRatio)
						} else {
							genErr = fmt.Errorf("image generation client not configured")
						}

						var doneItem map[string]any
						if genErr != nil {
							log.Printf("[Comini ImageGen] ❌ Error generating image: %v", genErr)
							doneItem = map[string]any{
								"id":        igcItemID,
								"call_id":   callID,
								"type":      "image_generation_call",
								"status":    "incomplete",
								"failure":   genErr.Error(),
							}
						} else {
							log.Printf("[Comini ImageGen] 🎨 Successfully generated image: saved to %s (dataURL len: %d)", savedPath, len(dataURL))
							doneItem = map[string]any{
								"id":             igcItemID,
								"call_id":        callID,
								"type":           "image_generation_call",
								"status":         "completed",
								"revised_prompt": prompt,
								"result":         dataURL,
								"saved_path":     savedPath,
							}
							allGeneratedImages = append(allGeneratedImages, generatedImageRecord{
								Prompt:    prompt,
								SavedPath: savedPath,
							})
						}
						completedOutputItems = append(completedOutputItems, doneItem)

						emitSSE("response.output_item.done", map[string]any{
							"output_index": fcIndex,
							"item":         doneItem,
						})
						flushSSE()

						// Server-side Autonomous Loop: Continue with Gemini if ContinueFn is provided (unlimited loops)
						if streamOpts.ContinueFn != nil {
							loopCount++
							obsText := fmt.Sprintf("[Observation: tool image_generation succeeded -> saved to: %s. To display this image to the user, include Markdown embed ![%s](%s) in your message]", savedPath, prompt, savedPath)
							if genErr != nil {
								obsText = fmt.Sprintf("[Observation: tool image_generation failed with error: %v]", genErr)
							}

							callCtx := streamOpts.Context
							if callCtx == nil {
								callCtx = context.Background()
							}
							newReader, cErr := streamOpts.ContinueFn(callCtx, domain.GeminiPart{
								FunctionCall:     part.FunctionCall,
								ThoughtSignature: sigToStore,
							}, obsText)

							if cErr == nil && newReader != nil {
								if curCloser, ok := currentReader.(io.Closer); ok {
									_ = curCloser.Close()
								}
								currentReader = newReader
								scanner = bufio.NewScanner(currentReader)
								scanner.Buffer(buf, 20*1024*1024)
								continue
							}
							log.Printf("[Comini Autonomous Loop] ⚠️ Failed to continue stream (%v), falling back to finishing turn", cErr)
						}

						// Fallback: If ContinueFn is not configured or failed, and no message has been emitted yet
						if !hasEmittedMessage {
							msgIndex := outputIndex
							outputIndex++
							msgID := randomID("msg")

							var msgText string
							if genErr != nil {
								msgText = fmt.Sprintf("❌ Không thể tạo ảnh: %v", genErr)
							} else {
								fileName := filepath.Base(savedPath)
								msgText = fmt.Sprintf("Tôi đã tạo ảnh thành công theo yêu cầu của bạn:\n\n![%s](%s)\n\n- **Prompt:** %s\n- **File đã lưu:** [%s](%s)",
									prompt, savedPath, prompt, fileName, savedPath)
							}

							emitSSE("response.output_item.added", map[string]any{
								"output_index": msgIndex,
								"item": map[string]any{
									"id":      msgID,
									"type":    "message",
									"role":    "assistant",
									"status":  "in_progress",
									"content": []any{},
								},
							})

							emitSSE("response.content_part.added", map[string]any{
								"output_index":  msgIndex,
								"item_id":       msgID,
								"content_index": 0,
								"part": map[string]any{
									"type":        "output_text",
									"text":        "",
									"annotations": []any{},
								},
							})

							emitSSE("response.output_text.delta", map[string]any{
								"output_index":  msgIndex,
								"item_id":       msgID,
								"content_index": 0,
								"delta":         msgText,
							})

							emitSSE("response.output_text.done", map[string]any{
								"output_index":  msgIndex,
								"item_id":       msgID,
								"content_index": 0,
								"text":          msgText,
							})

							emitSSE("response.content_part.done", map[string]any{
								"output_index":  msgIndex,
								"item_id":       msgID,
								"content_index": 0,
								"part": map[string]any{
									"type":        "output_text",
									"text":        msgText,
									"annotations": []any{},
								},
							})

							msgDoneItem := map[string]any{
								"id":     msgID,
								"type":   "message",
								"role":   "assistant",
								"status": "completed",
								"content": []any{
									map[string]any{
										"type":        "output_text",
										"text":        msgText,
										"annotations": []any{},
									},
								},
							}
							completedOutputItems = append(completedOutputItems, msgDoneItem)
							hasEmittedMessage = true

							emitSSE("response.output_item.done", map[string]any{
								"output_index": msgIndex,
								"item":         msgDoneItem,
							})
							flushSSE()
						}

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

	// If no message item was emitted yet (e.g. from an autonomous loop that ended without explicit text),
	// synthesize an assistant message containing all generated images.
	if !hasEmittedMessage && len(allGeneratedImages) > 0 {
		msgIndex := outputIndex
		outputIndex++
		msgID := randomID("msg")

		var b strings.Builder
		if len(allGeneratedImages) == 1 {
			img := allGeneratedImages[0]
			fileName := filepath.Base(img.SavedPath)
			b.WriteString(fmt.Sprintf("Tôi đã tạo ảnh thành công theo yêu cầu của bạn:\n\n![%s](%s)\n\n- **Prompt:** %s\n- **File đã lưu:** [%s](%s)",
				img.Prompt, img.SavedPath, img.Prompt, fileName, img.SavedPath))
		} else {
			b.WriteString(fmt.Sprintf("Tôi đã tạo thành công %d ảnh theo yêu cầu:\n\n", len(allGeneratedImages)))
			for idx, img := range allGeneratedImages {
				fileName := filepath.Base(img.SavedPath)
				b.WriteString(fmt.Sprintf("### Ảnh %d\n![%s](%s)\n- **Prompt:** %s\n- **File:** [%s](%s)\n\n",
					idx+1, img.Prompt, img.SavedPath, img.Prompt, fileName, img.SavedPath))
			}
		}
		msgText := b.String()

		emitSSE("response.output_item.added", map[string]any{
			"output_index": msgIndex,
			"item": map[string]any{
				"id":      msgID,
				"type":    "message",
				"role":    "assistant",
				"status":  "in_progress",
				"content": []any{},
			},
		})

		emitSSE("response.content_part.added", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			},
		})

		emitSSE("response.output_text.delta", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"delta":         msgText,
		})

		emitSSE("response.output_text.done", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"text":          msgText,
		})

		emitSSE("response.content_part.done", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        msgText,
				"annotations": []any{},
			},
		})

		msgDoneItem := map[string]any{
			"id":     msgID,
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []any{
				map[string]any{
					"type":        "output_text",
					"text":        msgText,
					"annotations": []any{},
				},
			},
		}
		completedOutputItems = append(completedOutputItems, msgDoneItem)
		hasEmittedMessage = true

		emitSSE("response.output_item.done", map[string]any{
			"output_index": msgIndex,
			"item":         msgDoneItem,
		})
		flushSSE()
	}

	if inputTokens == 0 {
		inputTokens = 1
	}
	if outputTokens == 0 {
		outputTokens = 1
	}

	// Safety net: if no actionable items (no message, no tool call) were emitted,
	// synthesize an assistant message so Codex never halts with an empty turn
	hasActionableItem := false
	for _, it := range completedOutputItems {
		t, _ := it["type"].(string)
		if t == "message" || t == "function_call" || t == "custom_tool_call" || t == "image_generation_call" || t == "tool_search_call" {
			hasActionableItem = true
			break
		}
	}

	if !hasActionableItem {
		msgIndex := outputIndex
		outputIndex++
		msgID := randomID("msg")

		fallbackText := "Đang tiếp tục xử lý..."
		if accumulatedReasoning.Len() > 0 {
			thoughtLines := strings.Split(strings.TrimSpace(accumulatedReasoning.String()), "\n")
			for i := len(thoughtLines) - 1; i >= 0; i-- {
				line := strings.TrimSpace(thoughtLines[i])
				if len(line) > 0 && len(line) < 300 && !strings.HasPrefix(line, "[") {
					fallbackText = line
					break
				}
			}
		}

		emitSSE("response.output_item.added", map[string]any{
			"output_index": msgIndex,
			"item": map[string]any{
				"id":      msgID,
				"type":    "message",
				"role":    "assistant",
				"status":  "in_progress",
				"content": []any{},
			},
		})
		emitSSE("response.content_part.added", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			},
		})
		emitSSE("response.output_text.delta", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"delta":         fallbackText,
		})
		emitSSE("response.output_text.done", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"text":          fallbackText,
		})
		emitSSE("response.content_part.done", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        fallbackText,
				"annotations": []any{},
			},
		})
		msgDoneItem := map[string]any{
			"id":     msgID,
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []any{
				map[string]any{
					"type":        "output_text",
					"text":        fallbackText,
					"annotations": []any{},
				},
			},
		}
		completedOutputItems = append(completedOutputItems, msgDoneItem)
		emitSSE("response.output_item.done", map[string]any{
			"output_index": msgIndex,
			"item":         msgDoneItem,
		})
		flushSSE()
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
					"cached_tokens": cachedTokens,
				},
				"output_tokens_details": map[string]any{
					"reasoning_tokens": reasoningTokens,
				},
			},
		},
	})

	var hitRate float64
	if inputTokens > 0 {
		hitRate = float64(cachedTokens) / float64(inputTokens) * 100
	}
	log.Printf("[Comini] 📊 Tokens: input=%d (cached=%d, hit=%.1f%%) output=%d reasoning=%d",
		inputTokens, cachedTokens, hitRate, outputTokens, reasoningTokens)

	// Finalize SSE stream
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flushSSE()

	return scanner.Err()
}

func extractActionFromText(text string) (string, map[string]any, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "[Action: called ") && !strings.HasPrefix(trimmed, "[Action: ") {
		return "", nil, false
	}
	rest := trimmed
	if strings.HasPrefix(rest, "[Action: called ") {
		rest = strings.TrimPrefix(rest, "[Action: called ")
	} else {
		rest = strings.TrimPrefix(rest, "[Action: ")
	}
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

	// Native support for raw custom tool input (e.g. apply_patch raw diff format)
	if (funcName == "apply_patch" || strings.HasSuffix(funcName, "apply_patch")) && strings.Contains(argsPart, "*** Begin Patch") {
		return funcName, map[string]any{"input": argsPart}, true
	}

	return "", nil, false
}

func extractImageGenFromText(text string) (string, map[string]any, bool) {
	trimmed := strings.TrimSpace(text)
	const prefix = "[Generated Image based on prompt:"
	if !strings.HasPrefix(trimmed, prefix) {
		return "", nil, false
	}
	rest := strings.TrimPrefix(trimmed, prefix)
	rest = strings.TrimSpace(rest)
	rest = strings.TrimSuffix(rest, "]")
	rest = strings.TrimSpace(rest)

	// Check if there is ". Saved to:" suffix
	if idx := strings.Index(rest, ". Saved to:"); idx != -1 {
		rest = strings.TrimSpace(rest[:idx])
	}

	prompt := rest
	if strings.HasPrefix(prompt, "\"") {
		if unquoted, err := strconv.Unquote(prompt); err == nil {
			prompt = unquoted
		} else {
			prompt = strings.Trim(prompt, "\"")
		}
	}

	if prompt == "" {
		return "", nil, false
	}

	aspectRatio := "1:1"
	lowerPrompt := strings.ToLower(prompt)
	if strings.Contains(lowerPrompt, "9:16") {
		aspectRatio = "9:16"
	} else if strings.Contains(lowerPrompt, "16:9") {
		aspectRatio = "16:9"
	} else if strings.Contains(lowerPrompt, "4:3") {
		aspectRatio = "4:3"
	} else if strings.Contains(lowerPrompt, "3:4") {
		aspectRatio = "3:4"
	}

	return "image_generation", map[string]any{
		"prompt":       prompt,
		"aspect_ratio": aspectRatio,
	}, true
}

func hasActionPrefix(s string) bool {
	return strings.HasPrefix(s, "[Action: called ") ||
		strings.HasPrefix(s, "[Action: ") ||
		strings.HasPrefix(s, "[Generated Image based on prompt:")
}

func mayBePartialActionPrefix(s string) bool {
	if len(s) == 0 {
		return false
	}
	const target = "[Action: called "
	if len(s) < len(target) && strings.HasPrefix(target, s) {
		return true
	}
	const alt = "[Action: "
	if len(s) < len(alt) && strings.HasPrefix(alt, s) {
		return true
	}
	const img = "[Generated Image based on prompt:"
	if len(s) < len(img) && strings.HasPrefix(img, s) {
		return true
	}
	return false
}

var stripProtocolPrefixes = []string{
	"[Observation:",
	"[Image omitted",
	"[Active Collaboration",
	"[Active Technical Plan",
	"[User Confirmed",
	"[Previous Conversation",
	"[Generated Image based on prompt:",
}

// findMatchingClosingBracket finds the index of the matching ']' for the '[' at startIndex.
// It properly tracks string literals, escaped quotes, and nested brackets.
// Returns -1 if no matching closing bracket is found in s.
func findMatchingClosingBracket(s string, startIndex int) int {
	if startIndex < 0 || startIndex >= len(s) || s[startIndex] != '[' {
		return -1
	}
	depth := 0
	inString := false
	escaped := false

	for i := startIndex; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if !inString {
			if ch == '[' {
				depth++
			} else if ch == ']' {
				depth--
				if depth == 0 {
					return i
				}
			}
		}
	}
	return -1
}

func hasProtocolPrefix(s string) (string, bool) {
	for _, p := range stripProtocolPrefixes {
		if strings.HasPrefix(s, p) {
			return p, true
		}
	}
	return "", false
}

func mayBePartialProtocolPrefix(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, p := range stripProtocolPrefixes {
		if len(s) < len(p) && strings.HasPrefix(p, s) {
			return true
		}
	}
	return false
}

type streamFilter struct {
	buf     string
	emitted int
}

func newStreamFilter() *streamFilter {
	return &streamFilter{}
}

func (f *streamFilter) feed(chunk string) (string, *domain.GeminiFunctionCall) {
	f.buf += chunk
	var ready strings.Builder
	var actionCall *domain.GeminiFunctionCall

	for len(f.buf) > 0 {
		openIdx := strings.IndexByte(f.buf, '[')
		if openIdx == -1 {
			ready.WriteString(f.buf)
			f.emitted += len(f.buf)
			f.buf = ""
			break
		}

		if openIdx > 0 {
			prefix := f.buf[:openIdx]
			if f.emitted == 0 && strings.TrimSpace(prefix) == "" {
				// Drop leading whitespace preceding a leading protocol or action block
				f.buf = f.buf[openIdx:]
			} else {
				ready.WriteString(prefix)
				f.emitted += len(prefix)
				f.buf = f.buf[openIdx:]
			}
		}

		// Now f.buf starts with '['
		// 1. Check for Action Call format: [Action: called <name> with <args>]
		if hasActionPrefix(f.buf) {
			closeIdx := findMatchingClosingBracket(f.buf, 0)
			if closeIdx == -1 {
				// Action block is still streaming across chunks; wait for closing ']'
				break
			}
			actionBlock := f.buf[:closeIdx+1]
			f.buf = f.buf[closeIdx+1:]
			if fnName, fnArgs, ok := extractActionFromText(actionBlock); ok {
				actionCall = &domain.GeminiFunctionCall{
					Name: fnName,
					Args: fnArgs,
				}
			} else if fnName, fnArgs, ok := extractImageGenFromText(actionBlock); ok {
				actionCall = &domain.GeminiFunctionCall{
					Name: fnName,
					Args: fnArgs,
				}
			}
			if f.emitted == 0 && ready.Len() == 0 {
				f.buf = strings.TrimLeft(f.buf, "\r\n")
			}
			if actionCall != nil {
				// Full action call extracted; return immediately so tool call is dispatched
				break
			}
			// Fallback: action block could not be extracted as a structured tool call.
			// NEVER DROP TEXT: deliver actionBlock as output text so model never goes silent!
			ready.WriteString(actionBlock)
			f.emitted += len(actionBlock)
			continue
		}

		if mayBePartialActionPrefix(f.buf) {
			// Incomplete prefix that could turn into an action call; buffer it
			break
		}

		// 2. Check for Leaked Protocol Observations to strip: [Observation: ...]
		if _, hasPrefix := hasProtocolPrefix(f.buf); hasPrefix {
			closeIdx := findMatchingClosingBracket(f.buf, 0)
			if closeIdx == -1 {
				// Protocol block is incomplete; wait for more stream chunks
				break
			}
			// Complete protocol block found -> drop it!
			f.buf = f.buf[closeIdx+1:]
			if f.emitted == 0 && ready.Len() == 0 {
				f.buf = strings.TrimLeft(f.buf, "\r\n")
			}
			continue
		}

		if mayBePartialProtocolPrefix(f.buf) {
			// Incomplete prefix that could turn into a protocol tag, buffer it
			break
		}

		// Starts with '[' but is NEITHER an Action NOR a Protocol tag (e.g. "[link](...", "[1]", "[x]")
		ready.WriteByte(f.buf[0])
		f.emitted++
		f.buf = f.buf[1:]
	}

	return ready.String(), actionCall
}

func (f *streamFilter) flush() string {
	if len(f.buf) == 0 {
		return ""
	}
	// Try parsing an action at EOF if missing trailing bracket:
	if hasActionPrefix(f.buf) {
		trimmed := strings.TrimSpace(f.buf)
		if !strings.HasSuffix(trimmed, "]") {
			trimmed += "]"
		}
		if _, _, ok := extractActionFromText(trimmed); ok {
			f.buf = ""
			return ""
		}
	}
	if _, hasPrefix := hasProtocolPrefix(f.buf); hasPrefix {
		// Pure protocol leak to strip (e.g. [Observation: ...])
		f.buf = ""
		return ""
	}
	rem := f.buf
	f.buf = ""
	f.emitted += len(rem)
	return rem
}

func (f *streamFilter) reset() {
	f.buf = ""
	f.emitted = 0
}

