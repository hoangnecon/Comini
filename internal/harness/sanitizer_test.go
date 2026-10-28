package harness

import (
	"strings"
	"testing"
)

func TestSanitizeSystemInstructionKaTeXGuidance(t *testing.T) {
	// Case 1: Instructions without KaTeX guidance
	input1 := "You are a helpful assistant."
	res1 := SanitizeSystemInstruction(&input1, nil)
	if res1 == nil {
		t.Fatal("expected non-nil result")
	}
	if !strings.Contains(*res1, "singleDollarTextMath: false") {
		t.Errorf("expected KaTeX guidance to be appended, got: %s", *res1)
	}

	// Case 2: Instructions already containing KaTeX guidance
	input2 := "You are a helpful assistant.\nKaTeX environment contract:\n- singleDollarTextMath: false"
	res2 := SanitizeSystemInstruction(&input2, nil)
	if res2 == nil {
		t.Fatal("expected non-nil result")
	}
	count := strings.Count(*res2, "singleDollarTextMath")
	if count != 1 {
		t.Errorf("expected exactly 1 occurrence of singleDollarTextMath, got %d", count)
	}
}
