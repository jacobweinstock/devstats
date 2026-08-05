package render

import (
	"strconv"
	"strings"
)

// Meta carries the header/summary context shared by all output formats.
type Meta struct {
	Org       string
	Since     string
	Until     string
	NumRepos  int
	NumTotal  int
	Breakdown bool
}

// sparkBar renders a width-char unicode block bar scaled so top fills it.
func sparkBar(value, top, width int) string {
	if top <= 0 || width <= 0 {
		return strings.Repeat(" ", max(width, 0))
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	units := float64(value) / float64(top) * float64(width)
	full := min(int(units), width)
	frac := units - float64(int(units))
	out := strings.Repeat("█", full)
	if full < width && frac > 0 {
		idx := min(int(frac*float64(len(blocks))), len(blocks)-1)
		out += string(blocks[idx])
		full++
	}
	if full < width {
		out += strings.Repeat(" ", width-full)
	}
	return out
}

// dot returns the count as text, or a placeholder for zero.
func dot(n int, placeholder string) string {
	if n == 0 {
		return placeholder
	}
	return strconv.Itoa(n)
}
