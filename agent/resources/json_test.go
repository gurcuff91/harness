package resources

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestResourcesJSON(t *testing.T) {
	for name, r := range map[string]Resources{"zero": {}, "empty skills": {Skills: []Skill{}}} {
		b, err := json.Marshal(r)
		if err != nil || string(b) != `{"skills":[]}` {
			t.Errorf("%s: %s, %v; want {\"skills\":[]}", name, b, err)
		}
		// also through a pointer
		if b, _ := json.Marshal(&r); string(b) != `{"skills":[]}` {
			t.Errorf("%s via pointer: %s", name, b)
		}
	}
	full := Resources{SystemMD: "sys", AgentsMD: "agents", Skills: []Skill{
		{Name: "dev", Description: "d", Location: "/x/SKILL.md", Content: "body"},
		{Name: "bare", Description: "b", Content: ""},
	}}
	b, _ := json.Marshal(full)
	want := `{"system_md":"sys","agents_md":"agents","skills":[{"name":"dev","description":"d","location":"/x/SKILL.md","content":"body"},{"name":"bare","description":"b","content":""}]}`
	if string(b) != want {
		t.Errorf("full = %s\nwant   %s", b, want)
	}
	var back Resources
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, full) {
		t.Errorf("round trip = %+v, %v; want %+v", back, err, full)
	}
	var empty Resources
	if err := json.Unmarshal([]byte(`{"skills":[]}`), &empty); err != nil || empty.SystemMD != "" || len(empty.Skills) != 0 {
		t.Errorf("empty round trip = %+v, %v", empty, err)
	}
}
