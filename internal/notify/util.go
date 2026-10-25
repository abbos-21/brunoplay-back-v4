package notify

import (
	"fmt"
	"strconv"
)

func sprint(v any) string          { return fmt.Sprint(v) }
func formatFloat(f float64) string { return strconv.FormatFloat(f, 'f', 6, 64) }

// formatInt prints a number with thousands separators: 1234567 -> 1,234,567.
func formatInt(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	out := make([]byte, 0, len(s)+len(s)/3)
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
