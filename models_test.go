package main

import (
	"strings"
	"testing"
)

func TestModelCatalogIDsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range modelCatalog {
		if spec.ID == "" || spec.Label == "" || spec.ModelName == "" {
			t.Fatalf("incomplete catalog entry: %#v", spec)
		}
		if seen[spec.ID] {
			t.Fatalf("duplicate model id %q", spec.ID)
		}
		seen[spec.ID] = true
	}
	if !seen[defaultModelID] {
		t.Fatalf("default model %q missing from catalog", defaultModelID)
	}
}

func TestDefaultModelSelection(t *testing.T) {
	sel := defaultModelSelection()
	if sel.Model != "claude-fable" || sel.Effort != effortMedium {
		t.Fatalf("default = %#v, want claude-fable/medium", sel)
	}
}

func TestMatchModel(t *testing.T) {
	spec, err := matchModel("claude-opus")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Label != "Claude Opus 5" || spec.ModelName != "opus" {
		t.Fatalf("claude-opus = %#v", spec)
	}
	spec, err = matchModel("Claude Fable")
	if err != nil {
		t.Fatal(err)
	}
	if spec.ID != "claude-fable" {
		t.Fatalf("label match id = %q", spec.ID)
	}
	if _, err := matchModel("nope"); err == nil {
		t.Fatal("expected unknown model error")
	}
}

func TestMatchEffort(t *testing.T) {
	e, err := matchEffort("xhigh")
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != effortXHigh || e.Label != "Extra High" {
		t.Fatalf("xhigh = %#v", e)
	}
	e, err = matchEffort("Extra High")
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != effortXHigh {
		t.Fatalf("label match = %q", e.ID)
	}
	e, err = matchEffort("max")
	if err != nil {
		t.Fatal(err)
	}
	if e.Label != "Max" {
		t.Fatalf("max label = %q", e.Label)
	}
	if _, err := matchEffort("ultra"); err == nil {
		t.Fatal("expected unknown effort error")
	}
}

func TestProviderEffortClamp(t *testing.T) {
	grok, ok := lookupModel("grok")
	if !ok {
		t.Fatal("missing grok")
	}
	if got := providerEffort(grok, effortMax); got != effortXHigh {
		t.Fatalf("grok max clamped to %q, want xhigh", got)
	}
	fable, ok := lookupModel("claude-fable")
	if !ok {
		t.Fatal("missing claude-fable")
	}
	if got := providerEffort(fable, effortMax); got != effortMax {
		t.Fatalf("fable max = %q, want max", got)
	}
	if got := providerEffort(fable, effortMedium); got != effortMedium {
		t.Fatalf("fable medium = %q", got)
	}
}

func TestPushMRU(t *testing.T) {
	got := pushMRU(nil, "a", 5)
	got = pushMRU(got, "b", 5)
	got = pushMRU(got, "c", 5)
	got = pushMRU(got, "b", 5)
	got = pushMRU(got, "d", 5)
	got = pushMRU(got, "e", 5)
	got = pushMRU(got, "f", 5)
	want := []string{"f", "e", "d", "b", "c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("MRU = %v, want %v", got, want)
	}
}

func TestRenderMenusHideIDs(t *testing.T) {
	sel := ModelSelection{Model: "claude-fable", Effort: effortMedium}
	menu := renderModelMenu(sel)
	if strings.Contains(menu, "claude-fable") {
		t.Fatalf("model menu leaked stable id:\n%s", menu)
	}
	if !strings.Contains(menu, "Current: Claude Fable, Medium") {
		t.Fatalf("model menu current:\n%s", menu)
	}
	if !strings.Contains(menu, "Choose a model:") {
		t.Fatalf("model menu prompt:\n%s", menu)
	}
	effort := renderEffortMenu(sel)
	if strings.Contains(effort, "xhigh") || strings.Contains(effort, "medium") {
		t.Fatalf("effort menu leaked id:\n%s", effort)
	}
	if !strings.Contains(effort, "Current: Medium") {
		t.Fatalf("effort menu current:\n%s", effort)
	}
	if !strings.Contains(effort, "Choose reasoning effort:") {
		t.Fatalf("effort menu prompt:\n%s", effort)
	}
	grokMax := renderEffortMenu(ModelSelection{Model: "grok", Effort: effortMax})
	if !strings.Contains(grokMax, "Extra High on Grok 4.6") {
		t.Fatalf("clamped effort:\n%s", grokMax)
	}
}

func TestSetSelectedModelUpdatesMRUOnly(t *testing.T) {
	setupTempStateForTest(t)
	initState()
	setSelectedModel("grok")
	setSelectedModel("claude-opus")
	setSelectedModel("grok")
	ReadState(func(s *State) {
		if s.Model != "grok" {
			t.Fatalf("model = %q", s.Model)
		}
		if strings.Join(s.RecentModels, ",") != "grok,claude-opus" {
			t.Fatalf("recent models = %v", s.RecentModels)
		}
	})
	setSelectedEffort(effortMax)
	ReadState(func(s *State) {
		if s.Effort != "max" {
			t.Fatalf("effort = %q", s.Effort)
		}
		if strings.Join(s.RecentEfforts, ",") != "max" {
			t.Fatalf("recent efforts = %v", s.RecentEfforts)
		}
	})
}

func TestCurrentModelSelectionDefaults(t *testing.T) {
	setupTempStateForTest(t)
	initState()
	sel := currentModelSelection()
	if sel.Model != "claude-fable" || sel.Effort != effortMedium {
		t.Fatalf("empty state selection = %#v", sel)
	}
	UpdateState(func(s *State) {
		s.Model = "not-a-model"
		s.Effort = "nope"
	})
	sel = currentModelSelection()
	if sel.Model != "claude-fable" || sel.Effort != effortMedium {
		t.Fatalf("invalid override selection = %#v", sel)
	}
}

func TestTelegramModelAndEffortCommands(t *testing.T) {
	setupTempStateForTest(t)
	initState()
	oldLookPath := lookPath
	lookPath = func(file string) (string, error) { return file, nil }
	t.Cleanup(func() { lookPath = oldLookPath })

	listing := applyModelCommand("")
	if !strings.Contains(listing, "Claude Fable") {
		t.Fatalf("/model listing:\n%s", listing)
	}
	if got := applyModelCommand("claude-opus"); got != "Using Claude Opus 5" {
		t.Fatalf("set by id: %q", got)
	}
	if got := applyModelCommand("Grok 4.6"); got != "Using Grok 4.6" {
		t.Fatalf("set by label: %q", got)
	}
	ReadState(func(s *State) {
		if s.Model != "grok" {
			t.Fatalf("state model = %q", s.Model)
		}
	})
	if got := applyEffortCommand("Extra High"); got != "Effort: Extra High" {
		t.Fatalf("set effort: %q", got)
	}
	if got := applyEffortCommand("max"); got != "Effort: Max" {
		t.Fatalf("set max: %q", got)
	}
	if got := applyModelCommand("nope"); !strings.Contains(got, "unknown model") {
		t.Fatalf("unknown model reply: %q", got)
	}
}
