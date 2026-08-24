package main

import (
	"fmt"
	"strings"
)

const (
	effortLow    EffortLevel = "low"
	effortMedium EffortLevel = "medium"
	effortHigh   EffortLevel = "high"
	effortXHigh  EffortLevel = "xhigh"
	effortMax    EffortLevel = "max"

	defaultModelID     = "claude-fable"
	defaultEffortLevel = effortMedium
	recentListCap      = 5
)

type EffortLevel string

type ModelSelection struct {
	Model  string
	Effort EffortLevel
}

type modelSpec struct {
	Label     string
	ID        string
	Agent     agentKind
	ModelName string
	MaxEffort EffortLevel
}

type effortSpec struct {
	ID    EffortLevel
	Label string
}

var modelCatalog = []modelSpec{
	{Label: "Claude Fable", ID: "claude-fable", Agent: agentClaude, ModelName: "fable", MaxEffort: effortMax},
	{Label: "Claude Opus 5", ID: "claude-opus", Agent: agentClaude, ModelName: "opus", MaxEffort: effortMax},
	{Label: "Claude Opus 4", ID: "claude-opus4", Agent: agentClaude, ModelName: "claude-opus-4-8", MaxEffort: effortMax},
	{Label: "Claude Sonnet 5", ID: "claude-sonnet", Agent: agentClaude, ModelName: "sonnet", MaxEffort: effortMax},
	{Label: "Codex GPT 5.6 Sol", ID: "codex-sol", Agent: agentCodex, ModelName: "gpt-5.6-sol", MaxEffort: effortMax},
	{Label: "Codex GPT 5.6 Terra", ID: "codex-terra", Agent: agentCodex, ModelName: "gpt-5.6-terra", MaxEffort: effortMax},
	{Label: "Codex GPT 5.6 Luna", ID: "codex-luna", Agent: agentCodex, ModelName: "gpt-5.6-luna", MaxEffort: effortMax},
	{Label: "Grok 4.6", ID: "grok", Agent: agentGrok, ModelName: "grok-4.6", MaxEffort: effortXHigh},
}

var effortCatalog = []effortSpec{
	{ID: effortLow, Label: "Low"},
	{ID: effortMedium, Label: "Medium"},
	{ID: effortHigh, Label: "High"},
	{ID: effortXHigh, Label: "Extra High"},
	{ID: effortMax, Label: "Max"},
}

func defaultModelSelection() ModelSelection {
	return ModelSelection{Model: defaultModelID, Effort: defaultEffortLevel}
}

func currentModelSelection() ModelSelection {
	sel := defaultModelSelection()
	ReadState(func(s *State) {
		if spec, ok := lookupModel(s.Model); ok {
			sel.Model = spec.ID
		}
		if e, ok := lookupEffort(s.Effort); ok {
			sel.Effort = e.ID
		}
	})
	return sel
}

func lookupModel(id string) (modelSpec, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return modelSpec{}, false
	}
	for _, spec := range modelCatalog {
		if spec.ID == id {
			return spec, true
		}
	}
	return modelSpec{}, false
}

func matchModel(s string) (modelSpec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return modelSpec{}, fmt.Errorf("missing model")
	}
	if spec, ok := lookupModel(s); ok {
		return spec, nil
	}
	folded := strings.ToLower(s)
	var matches []modelSpec
	for _, spec := range modelCatalog {
		if strings.ToLower(spec.Label) == folded {
			matches = append(matches, spec)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return modelSpec{}, fmt.Errorf("unknown model %q", s)
}

func lookupEffort(id string) (effortSpec, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return effortSpec{}, false
	}
	for _, spec := range effortCatalog {
		if string(spec.ID) == id {
			return spec, true
		}
	}
	return effortSpec{}, false
}

func matchEffort(s string) (effortSpec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return effortSpec{}, fmt.Errorf("missing effort")
	}
	if spec, ok := lookupEffort(s); ok {
		return spec, nil
	}
	folded := strings.ToLower(s)
	for _, spec := range effortCatalog {
		if strings.ToLower(spec.Label) == folded {
			return spec, nil
		}
	}
	return effortSpec{}, fmt.Errorf("unknown effort %q", s)
}

func effortIndex(e EffortLevel) int {
	for i, spec := range effortCatalog {
		if spec.ID == e {
			return i
		}
	}
	return -1
}

func providerEffort(spec modelSpec, requested EffortLevel) EffortLevel {
	if requested == "" {
		requested = defaultEffortLevel
	}
	req := effortIndex(requested)
	max := effortIndex(spec.MaxEffort)
	if req < 0 {
		requested = defaultEffortLevel
		req = effortIndex(requested)
	}
	if max < 0 {
		return requested
	}
	if req <= max {
		return requested
	}
	return spec.MaxEffort
}

func effortLabel(e EffortLevel) string {
	if spec, ok := lookupEffort(string(e)); ok {
		return spec.Label
	}
	return string(e)
}

func pushMRU(list []string, id string, limit int) []string {
	id = strings.TrimSpace(id)
	if id == "" {
		return list
	}
	out := make([]string, 0, limit)
	out = append(out, id)
	for _, x := range list {
		if x == id {
			continue
		}
		out = append(out, x)
		if len(out) == limit {
			break
		}
	}
	return out
}

func setSelectedModel(id string) {
	UpdateState(func(s *State) {
		s.Model = id
		s.RecentModels = pushMRU(s.RecentModels, id, recentListCap)
	})
}

func setSelectedEffort(e EffortLevel) {
	UpdateState(func(s *State) {
		s.Effort = string(e)
		s.RecentEfforts = pushMRU(s.RecentEfforts, string(e), recentListCap)
	})
}

func renderModelMenu(sel ModelSelection) string {
	current, ok := lookupModel(sel.Model)
	if !ok {
		current, _ = lookupModel(defaultModelID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Current: %s, %s\n", current.Label, effortLabel(sel.Effort))
	for _, spec := range modelCatalog {
		b.WriteByte('\n')
		if spec.ID == current.ID {
			b.WriteString("• ")
		}
		b.WriteString(spec.Label)
	}
	return b.String()
}

func applyModelCommand(rest string) string {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return renderModelMenu(currentModelSelection())
	}
	spec, err := matchModel(rest)
	if err != nil {
		return err.Error()
	}
	if hint := agentBinaryMissing(spec.Agent); hint != "" {
		return hint
	}
	setSelectedModel(spec.ID)
	return "Using " + spec.Label
}

func applyEffortCommand(rest string) string {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return renderEffortMenu(currentModelSelection())
	}
	e, err := matchEffort(rest)
	if err != nil {
		return err.Error()
	}
	setSelectedEffort(e.ID)
	return "Effort: " + e.Label
}

func renderEffortMenu(sel ModelSelection) string {
	spec, ok := lookupModel(sel.Model)
	if !ok {
		spec, _ = lookupModel(defaultModelID)
	}
	clamped := providerEffort(spec, sel.Effort)
	var b strings.Builder
	if clamped != sel.Effort {
		fmt.Fprintf(&b, "Current: %s (%s on %s)\n", effortLabel(sel.Effort), effortLabel(clamped), spec.Label)
	} else {
		fmt.Fprintf(&b, "Current: %s\n", effortLabel(sel.Effort))
	}
	for _, e := range effortCatalog {
		b.WriteByte('\n')
		if e.ID == sel.Effort {
			b.WriteString("• ")
		}
		b.WriteString(e.Label)
	}
	return b.String()
}
