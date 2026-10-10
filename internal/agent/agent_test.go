package agent

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/run"
)

func TestRegistryMatchesConfigKinds(t *testing.T) {
	var reg, cfg []string
	for k := range registry {
		reg = append(reg, k)
	}
	for k := range config.KnownAgentKinds {
		cfg = append(cfg, k)
	}
	sort.Strings(reg)
	sort.Strings(cfg)
	if strings.Join(reg, ",") != strings.Join(cfg, ",") {
		t.Errorf("agent registry %v != config.KnownAgentKinds %v", reg, cfg)
	}
}

func TestNewUnknownKind(t *testing.T) {
	if _, err := New("x", config.Agent{Kind: "gpt"}, run.Exec); err == nil {
		t.Fatal("want error for unknown kind")
	}
}

func TestNewClaude(t *testing.T) {
	r, err := New("sec", config.Agent{Kind: "claude"}, run.Exec)
	if err != nil || r.Name() != "sec" {
		t.Fatalf("New = %v, %v", r, err)
	}
}

func TestBuildPromptAppendsContract(t *testing.T) {
	p := BuildPrompt("Review carefully.")
	if !strings.HasPrefix(p, "Review carefully.\n\n") || !strings.HasSuffix(p, OutputContract) {
		t.Errorf("prompt = %q", p)
	}
}

func TestResultSchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal([]byte(ResultSchema), &v); err != nil {
		t.Fatalf("ResultSchema is not JSON: %v", err)
	}
}
