package format

import "fmt"

// Bytes formats a byte count as a human-readable string (e.g. "256M", "1G").
func Bytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.0fG", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// Memory formats used/total memory as a human-readable string.
func Memory(used, total int64) string {
	if total > 0 {
		return fmt.Sprintf("%s/%s", Bytes(used), Bytes(total))
	}
	return Bytes(used)
}
