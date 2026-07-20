package report

import (
	"strconv"
	"strings"
)

// FormatUSD keeps unavailable values distinct from a real zero balance while
// avoiding noisy trailing fractional zeros.
func FormatUSD(value *float64) string {
	if value == nil {
		return "不可用"
	}

	formatted := strconv.FormatFloat(*value, 'f', 6, 64)
	formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	if !strings.Contains(formatted, ".") {
		formatted += ".00"
	} else if len(formatted)-strings.LastIndex(formatted, ".") == 2 {
		formatted += "0"
	}
	return "$" + formatted
}
