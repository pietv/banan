package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// Mermaid writes a Mermaid state diagram of the allowed state
// transitions registered in fsm.New(...) to w.
func (b *Banan) Mermaid(w io.Writer) error {
	chart := b.state.Chart()

	// The chart is a map, so sort for stable output.
	names := make(map[string]struct{})
	for t := range chart {
		names[t.From] = struct{}{}
		names[t.To] = struct{}{}
	}
	start := b.state.Current()
	names[start] = struct{}{}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	slices.Sort(sorted)

	// State names contain spaces, so refer to states by id.
	ids := make(map[string]string, len(sorted))
	var sb strings.Builder
	sb.WriteString("stateDiagram-v2\n")
	for i, n := range sorted {
		ids[n] = fmt.Sprintf("s%d", i)
		fmt.Fprintf(&sb, "    %s: %s\n", ids[n], n)
	}
	fmt.Fprintf(&sb, "    [*] --> %s\n", ids[start])

	ts := make([]string, 0, len(chart))
	for t := range chart {
		ts = append(ts, fmt.Sprintf("    %s --> %s\n", ids[t.From], ids[t.To]))
	}
	slices.Sort(ts)
	for _, t := range ts {
		sb.WriteString(t)
	}

	_, err := io.WriteString(w, sb.String())
	return err
}
