package rules

import "testing"

func TestDetectAwaitingInput_YesNoPrompt(t *testing.T) {
	if !DetectAwaitingInput("Proceed with deletion? (y/n)") {
		t.Error("expected match for (y/n) prompt")
	}
}

func TestDetectAwaitingInput_DoYouWantTo(t *testing.T) {
	if !DetectAwaitingInput("Do you want to overwrite the existing config?") {
		t.Error("expected match for 'Do you want to...?' prompt")
	}
}

func TestDetectAwaitingInput_NormalOutput(t *testing.T) {
	if DetectAwaitingInput("Task completed successfully.") {
		t.Error("expected no match for normal completion message")
	}
}
