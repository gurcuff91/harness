package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSkillIncludesPath(t *testing.T) {
	readFn := func(name string) (string, string, error) {
		return "# Deploy\n\nRun scripts/deploy.sh to ship.", "/Users/x/.harness/agent/skills/deploy", nil
	}
	tool := Skill(readFn)
	in, _ := json.Marshal(map[string]string{"name": "deploy"})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	// The result must start with the location note before the content.
	if !strings.HasPrefix(out, "This skill is located at /Users/x/.harness/agent/skills/deploy") {
		t.Errorf("output should start with the location note:\n%s", out)
	}
	if !strings.Contains(out, "relative to this directory") {
		t.Errorf("output should explain relative paths:\n%s", out)
	}
	if !strings.Contains(out, "scripts/deploy.sh") {
		t.Errorf("output should include the skill content:\n%s", out)
	}
	// Location note must come BEFORE the content.
	if strings.Index(out, "located at") > strings.Index(out, "# Deploy") {
		t.Error("location note should precede the content")
	}
}
