package archive

import "testing"

var firstUserTextFixtures = []struct {
	name      string
	texts     []string
	want      string
	continued bool
}{
	{"datetime", []string{"<current_datetime>2026-11-19T01:00:00Z</current_datetime>\n\nFix the title"}, "Fix the title", false},
	{"claude", []string{"<system-reminder>Codebase and user instructions are shown below. IMPORTANT: follow CLAUDE.md.</system-reminder>", "<system-reminder>gitStatus: clean</system-reminder>", "Fix the archive titles"}, "Fix the archive titles", false},
	{"codex", []string{"# AGENTS.md instructions for /workspace\n<INSTRUCTIONS>Use Go.</INSTRUCTIONS>", "<environment_context><cwd>/workspace</cwd></environment_context>", "Please add pagination"}, "Please add pagination", false},
	{"inline context", []string{"<environment_context>cwd=/repo</environment_context>\n<userRequest>Keep my actual prompt</userRequest>"}, "Keep my actual prompt", false},
	{"continued", []string{"This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion.\nSummary: old private context", "<system-reminder>old tool result</system-reminder>", "Continue with the tests"}, "Continue with the tests", true},
	{"continued after prompt", []string{"The original prompt", "This session is being continued from a previous conversation that ran out of context."}, "The original prompt", true},
	{"continued only", []string{"This session is being continued from a previous conversation that ran out of context. Summary: old content"}, "", true},
	{"skill", []string{"<command-name>/poteto-mode</command-name><command-message>poteto-mode</command-message>", "Base directory for this skill: /plugins/poteto-mode\nSkill instructions...", "<skill><name>poteto-mode</name>injected body</skill>", "Fix search latency"}, "Fix search latency", false},
	{"command and prompt", []string{"<command-name>/review</command-name>\nReview the current changes", "Base directory for this skill: /plugins/review"}, "/review Review the current changes", false},
	{"slash args", []string{"<command-name>/p3-mode</command-name><command-args>was that the best long term approach?</command-args>", "Base directory for this skill: injected"}, "/p3-mode was that the best long term approach?", false},
	{"slash only", []string{"<command-name>/bro</command-name>"}, "/bro", false},
	{"t3 inline", []string{"continue from this: <t3_context>private injected body</t3_context>the failing check"}, "continue from this: the failing check", false},
	{"t3 notification", []string{"Delegated task node:delegated-task:command%3Abuild finished", "Fix the check"}, "Fix the check", false},
	{"t3 quote comments", []string{"[Assistant quote](t3-citation://thread/first?comment=first+point)\n[Assistant quote](t3-citation://thread/second?comment=second+point)"}, "first point\nsecond point", false},
	{"t3 quote", []string{"[Assistant quote](t3-citation://thread/item) Fix the check"}, "Fix the check", false},
	{"branch generation wrapper", []string{"You generate concise git branch names.\nUser message:\nhej"}, "hej", false},
	{"title generation wrapper", []string{"Generate a title.\nUser prompt:\nFix the graph layout"}, "Fix the graph layout", false},
}

func TestFirstUserText(t *testing.T) {
	for _, v := range firstUserTextFixtures {
		t.Run(v.name, func(t *testing.T) {
			got, continued := firstUserText(v.texts)
			if got != v.want || continued != v.continued {
				t.Fatalf("got %q continued=%v", got, continued)
			}
		})
	}
}
