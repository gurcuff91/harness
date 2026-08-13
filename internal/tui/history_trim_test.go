package tui

import (
	"testing"

	"github.com/gurcuff91/harness/types"
)

// msg builds a plain text message for trim tests.
func msg(text string) types.Message {
	return types.Message{Role: types.RoleUser, Parts: []types.ContentPart{{Text: text}}}
}

// compactMark builds a compaction-checkpoint marker message.
func compactMark() types.Message {
	return types.Message{
		Role: types.RoleUser,
		Meta: &types.MessageMeta{IsCompaction: true},
	}
}

func TestTrimToRecentHistory(t *testing.T) {
	tests := []struct {
		name       string
		in         []types.Message
		wantLen    int
		wantHidden int
	}{
		{
			name:       "no compactions renders everything",
			in:         []types.Message{msg("a"), msg("b"), msg("c")},
			wantLen:    3,
			wantHidden: 0,
		},
		{
			name:       "single compaction renders everything",
			in:         []types.Message{msg("a"), compactMark(), msg("b")},
			wantLen:    3,
			wantHidden: 0,
		},
		{
			// Marks at indices 2 and 5; second-to-last is index 2, cut is 2+1=3,
			// so we keep messages[3:] (4 messages) and hide the 3 ahead of them.
			// The first kept message is "c" (just past the checkpoint), not the
			// marker itself; the LAST checkpoint (index 5) is inside the range.
			name: "two compactions cuts just past second-to-last checkpoint",
			in: []types.Message{
				msg("a"), msg("b"),
				compactMark(), msg("c"), msg("d"),
				compactMark(), msg("e"),
			},
			wantLen:    4,
			wantHidden: 3,
		},
		{
			// Marks at 1, 3, 5; second-to-last is index 3, cut is 3+1=4, keep
			// messages[4:] (3), hide 4. First kept is "c", not a marker.
			name: "three compactions cuts just past second-to-last checkpoint",
			in: []types.Message{
				msg("a"),
				compactMark(), msg("b"),
				compactMark(), msg("c"),
				compactMark(), msg("d"),
			},
			wantLen:    3,
			wantHidden: 4,
		},
		{
			name:       "empty history",
			in:         nil,
			wantLen:    0,
			wantHidden: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, hidden := trimToRecentHistory(tc.in)
			if len(got) != tc.wantLen {
				t.Errorf("len = %d, want %d", len(got), tc.wantLen)
			}
			if hidden != tc.wantHidden {
				t.Errorf("hidden = %d, want %d", hidden, tc.wantHidden)
			}
			// When trimmed, the first kept message must NOT be a compaction
			// marker — we cut one past the second-to-last checkpoint precisely so
			// the history doesn't open with a bare "◎ Compacting" line.
			if tc.wantHidden > 0 {
				if m := got[0].Meta; m != nil && m.IsCompaction {
					t.Errorf("first kept message is a compaction marker; should be the one just after it")
				}
			}
		})
	}
}
