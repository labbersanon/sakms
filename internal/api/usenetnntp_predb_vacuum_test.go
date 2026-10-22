package api

import (
	"testing"
	"time"
)

func TestNextPreDBVacuum(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "before 05:15 same day",
			now:  time.Date(2026, 10, 23, 5, 14, 0, 0, loc),
			want: time.Date(2026, 10, 23, 5, 15, 0, 0, loc),
		},
		{
			name: "at 05:15 next day",
			now:  time.Date(2026, 10, 23, 5, 15, 0, 0, loc),
			want: time.Date(2026, 10, 24, 5, 15, 0, 0, loc),
		},
		{
			name: "after 05:15 next day",
			now:  time.Date(2026, 10, 23, 17, 0, 0, 0, loc),
			want: time.Date(2026, 10, 24, 5, 15, 0, 0, loc),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextPreDBVacuum(tc.now)
			if !got.Equal(tc.want) {
				t.Fatalf("got %s want %s", got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}
