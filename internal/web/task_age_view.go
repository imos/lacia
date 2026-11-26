package web

import (
	"math"
	"sort"
	"strconv"
	"time"
)

func sortByStatusSince(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].StatusSince.After(rows[j].StatusSince)
	})
}

func taskStatusAge(elapsed time.Duration) string {
	value, unit := elapsed.Minutes(), "分"
	switch {
	case elapsed >= 24*time.Hour:
		value, unit = elapsed.Hours()/24, "日"
	case elapsed >= time.Hour:
		value, unit = elapsed.Hours(), "時間"
	}
	value = max(0, value)
	if value < 9.95 {
		return strconv.FormatFloat(value, 'f', 1, 64) + unit + "前"
	}
	place := math.Pow10(max(0, int(math.Floor(math.Log10(value)))-1))
	return strconv.FormatFloat(math.Round(value/place)*place, 'f', 0, 64) + unit + "前"
}
