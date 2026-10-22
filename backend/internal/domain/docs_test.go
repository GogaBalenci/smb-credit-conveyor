package domain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Диаграмма в docs/state-machine.md должна содержать каждый переход из кода.
func TestStateMachineDocIsInSync(t *testing.T) {
	path := filepath.Join("..", "..", "..", "docs", "state-machine.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("docs not available: %v", err)
	}
	doc := string(raw)
	for _, tr := range Transitions() {
		for _, a := range tr.Actors {
			edge := string(tr.From) + " --> " + string(tr.To) + " : " + string(a)
			if !strings.Contains(doc, edge) {
				t.Errorf("docs/state-machine.md is missing transition %q", edge)
			}
		}
	}
}
