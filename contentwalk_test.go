package main

import "testing"

func TestFileSetMatches(t *testing.T) {
	set := FileSet{
		"AGENTS.md":       0,
		"foo/bar":         1,
		"RawInputs":       2,
		"RawInputs/a.md":  3,
		"RawInputs/b.txt": 4,
	}

	if !set.Matches("AGENTS.md") {
		t.Fatalf("expected Matches(AGENTS.md)=true")
	}
	if set.Matches("agents.md") {
		t.Fatalf("expected Matches(agents.md)=false (case-sensitive)")
	}
	if !set.Matches("foo/bar") {
		t.Fatalf("expected Matches(foo/bar)=true")
	}
	if set.Matches("foo/bar/baz.md") {
		t.Fatalf("expected Matches(foo/bar/baz.md)=false")
	}
}

func TestFileSetMatchesAnyParent(t *testing.T) {
	set := FileSet{
		"foo":     7,
		"bar/baz": 2,
		"x.md":    5,
	}

	cases := []struct {
		path string
		want int
	}{
		{"foo", 7},
		{"foo/a.md", 7},
		{"foobar/a.md", -1},
		{"foo-bar/a.md", -1},
		{"bar", -1},
		{"bar/baz", 2},
		{"bar/baz/qux.md", 2},
		{"bar/bazooka.md", -1},
		{"x.md", 5},
		{"x.md/child", 5},
		{"x.mdx", -1},
	}

	for _, tc := range cases {
		if got := set.MatchAnyParent(tc.path); got != tc.want {
			t.Fatalf("MatchAnyParent(%q)=%v, want %v", tc.path, got, tc.want)
		}
		if got := set.MatchesAnyParent(tc.path); got != (tc.want >= 0) {
			t.Fatalf("MatchesAnyParent(%q)=%v, want %v", tc.path, got, tc.want >= 0)
		}
	}
}
