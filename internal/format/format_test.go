package format

import "testing"

func TestBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{1, "1B"},
		{512, "512B"},
		{1023, "1023B"},
		{1024, "1K"},
		{1536, "2K"},           // rounds to nearest
		{1048576, "1M"},        // 1 MiB
		{1073741824, "1G"},     // 1 GiB
		{2147483648, "2G"},     // 2 GiB
		{10737418240, "10G"},   // 10 GiB
	}
	for _, tt := range tests {
		got := Bytes(tt.in)
		if got != tt.want {
			t.Errorf("Bytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMemory(t *testing.T) {
	tests := []struct {
		used, total int64
		want        string
	}{
		{0, 0, "0B"},
		{1048576, 0, "1M"},                     // used only, no total
		{268435456, 1073741824, "256M/1G"},      // 256M used of 1G
		{1073741824, 1073741824, "1G/1G"},       // full
		{512, 1024, "512B/1K"},                  // small values
	}
	for _, tt := range tests {
		got := Memory(tt.used, tt.total)
		if got != tt.want {
			t.Errorf("Memory(%d, %d) = %q, want %q", tt.used, tt.total, got, tt.want)
		}
	}
}
