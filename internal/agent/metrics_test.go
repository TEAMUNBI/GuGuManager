package agent

import "testing"

func TestParsePlayersFromRCON(t *testing.T) {
	cases := []struct {
		name   string
		output string
		online int
		max    int
	}{
		{"standard", "There are 2 of a max of 20 players online: alice, bob", 2, 20},
		{"empty", "There are 0 of a max of 20 players online:", 0, 20},
		{"malformed", "[21:32:10 INFO]: Console: No players found", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			online, max := parsePlayersFromRCON(tc.output)
			if online != tc.online || max != tc.max {
				t.Errorf("parsePlayersFromRCON(%q) = (%d, %d), want (%d, %d)", tc.output, online, max, tc.online, tc.max)
			}
		})
	}
}
