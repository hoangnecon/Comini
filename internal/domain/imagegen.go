package domain

import (
	"strings"
)

// ImageGenArgs is the full argument set of Antigravity 2.0's `generate_image` tool
// (struct { Prompt, ImageName, AspectRatio string; ImagePaths []string } in the AGY hub binary).
type ImageGenArgs struct {
	// Prompt is the text prompt, or the edit instructions when ImagePaths is set.
	Prompt string
	// ImageName is a short lowercase_with_underscores name used for the saved file.
	ImageName string
	// AspectRatio is one of SupportedImageAspectRatios; empty means the default (1:1).
	AspectRatio string
	// ImagePaths are local source/reference images to edit, combine, or use as references (max 3).
	ImagePaths []string
	// BaseDir resolves relative ImagePaths (the client's working directory when known).
	BaseDir string
}

// MaxImageReferencePaths mirrors AGY's `jsonschema:"maxItems=3"` on ImagePaths.
const MaxImageReferencePaths = 3

// SupportedImageAspectRatios mirrors the enum in AGY's generate_image schema
// ("1:1, 2:3, 3:2, 3:4, 4:3, 9:16, 16:9"; default 1:1).
var SupportedImageAspectRatios = []string{"1:1", "2:3", "3:2", "3:4", "4:3", "9:16", "16:9"}

// NormalizeAspectRatio returns a supported aspect ratio, or "" (default) when unsupported.
func NormalizeAspectRatio(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	s = strings.ReplaceAll(s, "x", ":")
	for _, ok := range SupportedImageAspectRatios {
		if s == ok {
			return ok
		}
	}
	return ""
}

func firstString(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ParseImageGenArgs extracts generate_image arguments from a model function call. It accepts
// the snake_case, camelCase and PascalCase spellings that appear across AGY protos and the
// Codex skill docs, plus legacy aliases (description/query) used by older Comini sessions.
func ParseImageGenArgs(args map[string]any) ImageGenArgs {
	out := ImageGenArgs{
		Prompt:      firstString(args, "prompt", "Prompt", "description", "query"),
		ImageName:   firstString(args, "image_name", "imageName", "ImageName"),
		AspectRatio: NormalizeAspectRatio(firstString(args, "aspect_ratio", "aspectRatio", "AspectRatio")),
	}
	for _, k := range []string{"image_paths", "imagePaths", "ImagePaths"} {
		switch v := args[k].(type) {
		case []any:
			for _, e := range v {
				if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
					out.ImagePaths = append(out.ImagePaths, strings.TrimSpace(s))
				}
			}
		case []string:
			for _, s := range v {
				if strings.TrimSpace(s) != "" {
					out.ImagePaths = append(out.ImagePaths, strings.TrimSpace(s))
				}
			}
		case string:
			if strings.TrimSpace(v) != "" {
				out.ImagePaths = append(out.ImagePaths, strings.TrimSpace(v))
			}
		}
		if len(out.ImagePaths) > 0 {
			break
		}
	}
	if len(out.ImagePaths) > MaxImageReferencePaths {
		out.ImagePaths = out.ImagePaths[:MaxImageReferencePaths]
	}
	return out
}
