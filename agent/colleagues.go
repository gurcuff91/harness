package agent

import (
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/internal/config"
)

// colleagueDirectory is the tools.ColleagueDirectory the colleague tools are
// wired with: the live instance registry from the settings store (written by
// the server package on every Serve/Close), read fresh on every call.
func colleagueDirectory() map[string]tools.Colleague {
	out := map[string]tools.Colleague{}
	for name, e := range config.GetSettingsManager().Instances() {
		out[name] = tools.Colleague{
			Version:   e.Version,
			Transport: e.Transport,
			URL:       e.URL,
			CWD:       e.CWD,
			PID:       e.PID,
		}
	}
	return out
}
