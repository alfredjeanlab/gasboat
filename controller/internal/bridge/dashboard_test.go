package bridge

import (
	"testing"

	"gasboat/controller/internal/beadsapi"
)

func TestBuildDashboardHash_DistinguishesDoneFromIdle(t *testing.T) {
	idle := []beadsapi.AgentBead{
		{ID: "bd-1", AgentName: "bot-1", AgentState: "", Project: "gasboat"},
	}
	done := []beadsapi.AgentBead{
		{ID: "bd-1", AgentName: "bot-1", AgentState: "done", Project: "gasboat"},
	}
	rateLimited := []beadsapi.AgentBead{
		{ID: "bd-1", AgentName: "bot-1", AgentState: "rate_limited", Project: "gasboat"},
	}

	hashIdle := buildDashboardHash(idle, nil)
	hashDone := buildDashboardHash(done, nil)
	hashRateLimited := buildDashboardHash(rateLimited, nil)

	if hashIdle == hashDone {
		t.Errorf("idle and done should produce different hashes, both got %q", hashIdle)
	}
	if hashIdle == hashRateLimited {
		t.Errorf("idle and rate_limited should produce different hashes, both got %q", hashIdle)
	}
	if hashDone == hashRateLimited {
		t.Errorf("done and rate_limited should produce different hashes, both got %q", hashDone)
	}
}

func TestBuildDashboardHash_DistinguishesFailedFromDone(t *testing.T) {
	failed := []beadsapi.AgentBead{
		{ID: "bd-1", AgentName: "bot-1", AgentState: "failed", Project: "gasboat"},
	}
	done := []beadsapi.AgentBead{
		{ID: "bd-1", AgentName: "bot-1", AgentState: "done", Project: "gasboat"},
	}

	hashFailed := buildDashboardHash(failed, nil)
	hashDone := buildDashboardHash(done, nil)

	if hashFailed == hashDone {
		t.Errorf("failed and done should produce different hashes, both got %q", hashFailed)
	}
}
