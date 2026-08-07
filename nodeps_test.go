package converge

import (
	"os"
	"strings"
	"testing"
)

func TestNoDependencies(t *testing.T) {
	src, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	inBlock := false
	for n, raw := range strings.Split(string(src), "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "":
		case inBlock && line == ")":
			inBlock = false
		case inBlock:
			t.Errorf("go.mod:%d: dependency %s", n+1, line)
		case line == "require (":
			inBlock = true
		case strings.HasPrefix(line, "require "):
			t.Errorf("go.mod:%d: %s", n+1, line)
		}
	}
	if inBlock {
		t.Error("go.mod: unterminated require block")
	}
}
