package worker

import "testing"

func TestParseCodexCommandActivityWithoutOutput(t *testing.T) {
	line := []byte(`{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"/bin/zsh -lc npm test","aggregated_output":"secret repository output","exit_code":0,"status":"completed"}}`)
	activity, ok := parseCodexActivity(line, "/workspace/repo")
	if !ok || activity.Type != "codex.command.completed" || activity.Message != "Ran command: npm test" {
		t.Fatalf("unexpected activity: %+v ok=%v", activity, ok)
	}
	if activity.ExitCode == nil || *activity.ExitCode != 0 {
		t.Fatalf("missing exit code: %+v", activity)
	}
}

func TestParseCodexFileActivityUsesRepoRelativePaths(t *testing.T) {
	line := []byte(`{"type":"item.completed","item":{"id":"item_2","type":"file_change","changes":[{"path":"/workspace/repo/apps/web/src/App.tsx","kind":"update"}],"status":"completed"}}`)
	activity, ok := parseCodexActivity(line, "/workspace/repo")
	if !ok || activity.Message != "Edited apps/web/src/App.tsx" || len(activity.Paths) != 1 {
		t.Fatalf("unexpected activity: %+v ok=%v", activity, ok)
	}
}

func TestCommandActivityRedactsCredentialArguments(t *testing.T) {
	got := safeCommandSummary(`curl --token super-secret-value -H 'Authorization: Bearer abcdefghijklmnop' example.test`)
	if got == "" || containsAny(got, "super-secret-value", "abcdefghijklmnop") {
		t.Fatalf("credential leaked in %q", got)
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if len(needle) > 0 && len(value) >= len(needle) {
			for index := 0; index+len(needle) <= len(value); index++ {
				if value[index:index+len(needle)] == needle {
					return true
				}
			}
		}
	}
	return false
}
