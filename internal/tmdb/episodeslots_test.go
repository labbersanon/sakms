package tmdb

import "testing"

func TestSlotByAirDate_Unique(t *testing.T) {
	slots := []EpisodeSlot{
		{Season: 1, Episode: 1, AirDate: "2024-03-14"},
		{Season: 1, Episode: 2, AirDate: "2024-03-15"},
	}
	s, e, ok := SlotByAirDate(slots, "2024-03-15")
	if !ok || s != 1 || e != 2 {
		t.Fatalf("got (%d, %d, %v)", s, e, ok)
	}
}

func TestSlotByAirDate_Ambiguous(t *testing.T) {
	slots := []EpisodeSlot{
		{Season: 1, Episode: 1, AirDate: "2024-03-15"},
		{Season: 1, Episode: 2, AirDate: "2024-03-15"},
	}
	if _, _, ok := SlotByAirDate(slots, "2024-03-15"); ok {
		t.Fatal("two episodes on the same date must refuse")
	}
}

func TestSlotByAbsolute_SkipsSpecials(t *testing.T) {
	slots := []EpisodeSlot{
		{Season: 0, Episode: 1, AirDate: "2019-01-01"},
		{Season: 1, Episode: 1, AirDate: "2020-01-01"},
		{Season: 1, Episode: 2, AirDate: "2020-01-08"},
		{Season: 2, Episode: 1, AirDate: "2021-01-01"},
	}
	s, e, ok := SlotByAbsolute(slots, 1)
	if !ok || s != 1 || e != 1 {
		t.Fatalf("abs 1 = (%d, %d, %v), want S01E01", s, e, ok)
	}
	s, e, ok = SlotByAbsolute(slots, 3)
	if !ok || s != 2 || e != 1 {
		t.Fatalf("abs 3 = (%d, %d, %v), want S02E01", s, e, ok)
	}
	if _, _, ok := SlotByAbsolute(slots, 99); ok {
		t.Fatal("past-the-end absolute must refuse")
	}
}
