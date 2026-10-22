package tests

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"testing"

	"comini/internal/domain"
	"comini/internal/transport"
)

func dims(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return err.Error()
	}
	defer f.Close()
	c, _, err := image.DecodeConfig(f)
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("%dx%d", c.Width, c.Height)
}

func TestLiveCCAImageParams(t *testing.T) {
	if os.Getenv("COMINI_LIVE_TESTS") != "1" {
		t.Skip()
	}
	token, proj, err := transport.DefaultAuthManager.GetCredentials()
	if err != nil || token == "" {
		t.Skipf("no auth: %v", err)
	}
	c := transport.NewCCAClient("", "antigravity")
	ctx := context.Background()
	p1, _, err := c.GenerateImage(ctx, domain.ImageGenArgs{Prompt: "A simple flat illustration of a red fox sitting in a snowy forest", ImageName: "red_fox_snow", AspectRatio: "16:9"}, "Bearer "+token, proj)
	fmt.Println("LIVE1", p1, dims(p1), err)
	if err != nil {
		t.Fatal(err)
	}
	p2, _, err := c.GenerateImage(ctx, domain.ImageGenArgs{Prompt: "Edit this image: make it night time with a full moon, keep the same fox and composition", ImageName: "red_fox_night", AspectRatio: "9:16", ImagePaths: []string{p1}}, "Bearer "+token, proj)
	fmt.Println("LIVE2", p2, dims(p2), err)
	if err != nil {
		t.Fatal(err)
	}
}
