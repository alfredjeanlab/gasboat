package main

// prime_shared.go contains the outputPrimeForHook function shared by
// bus_emit.go (SessionStart injection) and hook.go (hook prime command).

import (
	"fmt"
	"io"
	"os"
)

// outputPrimeForHook generates prime output wrapped in a system-reminder tag.
// This is called by bus emit on SessionStart and by hook prime.
func outputPrimeForHook(w io.Writer, agentID string) {
	fmt.Fprintln(w, "<system-reminder>")

	// Thread context — prepended for thread-spawned agents.
	outputSlackThreadContext(w)

	outputWorkflowContext(w)
	if agentID != "" {
		outputAdvice(w, agentID)
	}
	outputJackSection(w)
	outputRosterSection(w, agentID)
	if agentID != "" {
		outputAutoAssign(w, agentID)
	}
	fmt.Fprintln(w, "</system-reminder>")
}

// outputSlackThreadContext fetches and outputs the originating Slack thread
// context for thread-spawned agents. No-op if the agent was not spawned from
// a Slack thread or if the bridge is unreachable.
func outputSlackThreadContext(w io.Writer) {
	if !isThreadSpawnedAgent() {
		return
	}

	bridgeURL := os.Getenv("SLACK_BRIDGE_URL")
	if bridgeURL == "" {
		return
	}
	channel := os.Getenv("SLACK_THREAD_CHANNEL")
	threadTS := os.Getenv("SLACK_THREAD_TS")

	msgs := fetchThreadMessages(bridgeURL, channel, threadTS, 50)
	if len(msgs) == 0 {
		return
	}

	fmt.Fprintln(w, "## Slack Thread Context")
	fmt.Fprintln(w, "You were spawned to help with this Slack thread:")
	fmt.Fprintln(w)
	fmt.Fprint(w, formatThreadMarkdown(msgs, 4000))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Reply to the thread using `gb slack reply` for conversational responses.")
	fmt.Fprintln(w, "---")
	fmt.Fprintln(w)
}
