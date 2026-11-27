package resources

import "testing"

// Nil discovers nothing, whatever the directory.
func TestNilLoaderDiscoversNothing(t *testing.T) {
	res, err := Nil().Load(t.TempDir())
	if err != nil || res == nil {
		t.Fatalf("Load = %v, %v; want empty Resources, nil", res, err)
	}
	if res.SystemMD != "" || res.AgentsMD != "" || len(res.Skills) != 0 {
		t.Errorf("Resources = %+v, want empty", res)
	}
}
