package agent

import "github.com/gurcuff91/harness/agent/schedule"

// FireScheduleForTest fires a schedule now, exactly as the engine would when
// it is due (the engine only fires on the minute). For tests of other
// packages (channels) that drive a schedule end to end; not part of the
// supported API.
func (a *Agent) FireScheduleForTest(sc schedule.Schedule) { a.fireScheduledPrompt(sc) }
