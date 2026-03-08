package bridge

import (
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack"
)

func TestExtractImageTag(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"full reference", "ghcr.io/org/agent:v2026.58.3", "v2026.58.3"},
		{"latest tag", "ghcr.io/org/agent:latest", "latest"},
		{"bare tag", "v2026.58.3", "v2026.58.3"},
		{"empty", "", ""},
		{"no tag", "ghcr.io/org/agent", "ghcr.io/org/agent"},
		{"port in registry", "registry:5000/img:v1", "v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractImageTag(tc.input); got != tc.want {
				t.Errorf("extractImageTag(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestBuildAgentCardBlocks_ImageTagInContext(t *testing.T) {
	blocks := buildAgentCardBlocks(
		"gasboat/crew/test-bot",
		0,        // pending
		"working", // state
		"",       // taskTitle
		time.Time{}, // seen (zero)
		"",       // coopmuxURL
		"",       // podName
		"v2026.58.3", // imageTag
		"",       // role
	)

	// The context block (index 1) should contain the image tag.
	if len(blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(blocks))
	}

	// Render the context block text to check for the image tag.
	// Context blocks contain TextBlockObject elements.
	contextBlock := blocks[1]
	rendered := contextBlock.BlockType()
	if rendered != "context" {
		t.Fatalf("expected context block at index 1, got %q", rendered)
	}

	// Verify the image tag appears in the agent card context.
	// We check all text elements in the context block.
	found := false
	for _, elem := range contextBlock.(*slack.ContextBlock).ContextElements.Elements {
		if textObj, ok := elem.(*slack.TextBlockObject); ok {
			if strings.Contains(textObj.Text, "v2026.58.3") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("expected image tag v2026.58.3 in context block text")
	}
}

func TestBuildAgentCardBlocks_NoImageTag(t *testing.T) {
	blocks := buildAgentCardBlocks(
		"gasboat/crew/test-bot",
		0,
		"working",
		"",
		time.Time{},
		"",
		"",
		"", // no imageTag
		"", // role
	)

	if len(blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(blocks))
	}

	contextBlock := blocks[1].(*slack.ContextBlock)
	for _, elem := range contextBlock.ContextElements.Elements {
		if textObj, ok := elem.(*slack.TextBlockObject); ok {
			// The context should still have the bead ID and thread line, but no image tag separator after "Decisions thread below".
			if strings.Contains(textObj.Text, "Decisions thread below ·") && !strings.Contains(textObj.Text, "Decisions thread below · _") {
				// If there's a dot after "Decisions thread below" and it's not the seen timestamp,
				// then something is wrong — an empty tag might have been appended.
				t.Error("unexpected separator after 'Decisions thread below' when imageTag is empty")
			}
		}
	}
}

func TestBuildAgentCardBlocks_RoleInHeader(t *testing.T) {
	blocks := buildAgentCardBlocks(
		"gasboat/crew/test-bot",
		0,           // pending
		"working",   // state
		"",          // taskTitle
		time.Time{}, // seen (zero)
		"",          // coopmuxURL
		"",          // podName
		"",          // imageTag
		"lead",      // role
	)

	if len(blocks) < 1 {
		t.Fatalf("expected at least 1 block, got %d", len(blocks))
	}

	// The header section (index 0) should contain the role.
	headerBlock, ok := blocks[0].(*slack.SectionBlock)
	if !ok {
		t.Fatalf("expected section block at index 0, got %T", blocks[0])
	}
	if !strings.Contains(headerBlock.Text.Text, "lead") {
		t.Errorf("expected role 'lead' in header text, got %q", headerBlock.Text.Text)
	}
}

func TestBuildAgentCardBlocks_MultiRole(t *testing.T) {
	blocks := buildAgentCardBlocks(
		"gasboat/crew/test-bot",
		0, "working", "", time.Time{}, "", "", "",
		"crew,thread", // multi-role
	)

	headerBlock := blocks[0].(*slack.SectionBlock)
	if !strings.Contains(headerBlock.Text.Text, "crew,thread") {
		t.Errorf("expected multi-role 'crew,thread' in header, got %q", headerBlock.Text.Text)
	}
}

func TestBuildAgentCardBlocks_NoRole(t *testing.T) {
	blocks := buildAgentCardBlocks(
		"gasboat/crew/test-bot",
		0, "working", "", time.Time{}, "", "", "",
		"", // no role
	)

	headerBlock := blocks[0].(*slack.SectionBlock)
	// Should have project and status but no extra separator for empty role.
	// Format: ":large_green_circle: *test-bot* · _gasboat_ · working"
	if strings.Count(headerBlock.Text.Text, "·") != 2 {
		t.Errorf("expected 2 separators (project + status) with no role, got %q", headerBlock.Text.Text)
	}
}

func TestBuildWrapUpAgentCardBlocks_Done(t *testing.T) {
	wrapupJSON := `{"accomplishments":"Closed 3 bugs","blockers":"API key pending"}`
	blocks := buildWrapUpAgentCardBlocks("gasboat/crew/test-bot", "done", wrapupJSON)

	// Should have 3 blocks: header, wrapup section, action (Clear button).
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}

	// First block: header with agent name and state.
	header := blocks[0].(*slack.SectionBlock)
	if !strings.Contains(header.Text.Text, "test-bot") {
		t.Errorf("expected agent name in header, got: %s", header.Text.Text)
	}
	if !strings.Contains(header.Text.Text, ":white_check_mark:") {
		t.Errorf("expected done indicator in header, got: %s", header.Text.Text)
	}
	if !strings.Contains(header.Text.Text, "done") {
		t.Errorf("expected 'done' state in header, got: %s", header.Text.Text)
	}

	// Second block: wrapup content.
	wrapup := blocks[1].(*slack.SectionBlock)
	if !strings.Contains(wrapup.Text.Text, "Accomplishments") {
		t.Errorf("expected accomplishments in wrapup block, got: %s", wrapup.Text.Text)
	}
	if !strings.Contains(wrapup.Text.Text, "Blockers") {
		t.Errorf("expected blockers in wrapup block, got: %s", wrapup.Text.Text)
	}

	// Third block: action with Clear button.
	if blocks[2].BlockType() != "actions" {
		t.Errorf("expected actions block at index 2, got %q", blocks[2].BlockType())
	}
}

func TestBuildWrapUpAgentCardBlocks_Failed(t *testing.T) {
	wrapupJSON := `{"accomplishments":"Partial work done"}`
	blocks := buildWrapUpAgentCardBlocks("test-bot", "failed", wrapupJSON)

	header := blocks[0].(*slack.SectionBlock)
	if !strings.Contains(header.Text.Text, ":x:") {
		t.Errorf("expected failed indicator in header, got: %s", header.Text.Text)
	}
}

func TestBuildWrapUpAgentCardBlocks_EmptyWrapUp(t *testing.T) {
	blocks := buildWrapUpAgentCardBlocks("test-bot", "done", `{}`)

	// Empty wrapup: header + action only (no wrapup section block).
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks for empty wrapup, got %d", len(blocks))
	}
}

func TestFormatWrapUpSlack_FullWrapUp(t *testing.T) {
	wrapupJSON := `{"accomplishments":"Closed 3 bugs","blockers":"API key pending","handoff_notes":"Check PR #42","beads_closed":["kd-1","kd-2"],"pull_requests":["https://github.com/org/repo/pull/42"]}`
	result := formatWrapUpSlack(wrapupJSON)

	if !strings.Contains(result, "*Accomplishments:* Closed 3 bugs") {
		t.Errorf("expected accomplishments in output, got: %s", result)
	}
	if !strings.Contains(result, "*Blockers:* API key pending") {
		t.Errorf("expected blockers in output, got: %s", result)
	}
	if !strings.Contains(result, "*Handoff:* Check PR #42") {
		t.Errorf("expected handoff notes in output, got: %s", result)
	}
	if !strings.Contains(result, "*Beads closed:* kd-1, kd-2") {
		t.Errorf("expected beads closed in output, got: %s", result)
	}
	if !strings.Contains(result, "*PRs:* https://github.com/org/repo/pull/42") {
		t.Errorf("expected pull requests in output, got: %s", result)
	}
}

func TestFormatWrapUpSlack_AccomplishmentsOnly(t *testing.T) {
	wrapupJSON := `{"accomplishments":"Fixed login flow"}`
	result := formatWrapUpSlack(wrapupJSON)

	if !strings.Contains(result, "*Accomplishments:* Fixed login flow") {
		t.Errorf("expected accomplishments in output, got: %s", result)
	}
	if strings.Contains(result, "Blockers") {
		t.Errorf("should not contain blockers when empty, got: %s", result)
	}
}

func TestFormatWrapUpSlack_InvalidJSON(t *testing.T) {
	result := formatWrapUpSlack("not valid json")
	if !strings.Contains(result, "not valid json") {
		t.Errorf("expected raw text fallback for invalid JSON, got: %s", result)
	}
}

func TestFormatWrapUpSlack_EmptyWrapUp(t *testing.T) {
	result := formatWrapUpSlack(`{}`)
	if result != "" {
		t.Errorf("expected empty string for empty wrapup, got: %q", result)
	}
}
