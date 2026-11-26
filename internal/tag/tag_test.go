package tag

import "testing"

func TestParseAndCompare(t *testing.T) {
	a, err := ParseVersion("v0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if a.String() != "v0.0.1" {
		t.Fatalf("String() = %q", a.String())
	}

	b, err := ParseVersion("v0.0.10")
	if err != nil {
		t.Fatal(err)
	}
	if b.Compare(a) <= 0 {
		t.Fatal("v0.0.10 should sort after v0.0.1")
	}

	pre, err := ParseVersion("v1.0.0-rc.1")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := ParseVersion("v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if pre.Compare(rel) >= 0 {
		t.Fatal("a pre-release must sort before its release")
	}
}

func TestParseVersionRejectsNonVersions(t *testing.T) {
	for _, raw := range []string{
		"hello/v0.0.1",      // a package tag is not a repository version
		"addition@v0.0.1",   // neither is the @ form
		"v0.0",              // not semver
		"0.0.1",             // no leading v
		"addition/v0.0.1/x", // extra segment
		"",                  // empty
	} {
		if _, err := ParseVersion(raw); err == nil {
			t.Errorf("ParseVersion(%q) should fail", raw)
		}
	}
}

func TestLatestIgnoresPackageTags(t *testing.T) {
	// One repository is one package (D83): only plain vX.Y.Z tags count. A
	// prefixed tag is ignored, because it would be ambiguous.
	tags := []string{
		"hello/v5.0.0",
		"addition/v0.0.1",
		"v0.0.3",
		"addition/v0.0.2",
	}
	got, err := Latest(tags)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "v0.0.3" {
		t.Fatalf("Latest = %q, want v0.0.3", got.String())
	}
}

func TestFindReturnsTheTagThatWasUsed(t *testing.T) {
	if name, v, err := Find("v0.0.2", []string{"v0.0.1", "v0.0.2"}); err != nil || name != "v0.0.2" || v.String() != "v0.0.2" {
		t.Fatalf("repo-wide: %q %v %v", name, v, err)
	}
	if _, _, err := Find("0.0.1", []string{"addition/v0.0.1"}); err == nil {
		t.Fatal("a package tag must not satisfy a repository version")
	}
}

func TestMatch(t *testing.T) {
	tags := []string{"v0.0.1", "v0.0.2", "addition/v9.9.9"}

	if v, err := Match("latest", tags); err != nil || v.String() != "v0.0.2" {
		t.Fatalf("latest: %v %v", v, err)
	}
	if v, err := Match("v0.0.1", tags); err != nil || v.String() != "v0.0.1" {
		t.Fatalf("exact with v: %v %v", v, err)
	}
	if v, err := Match("0.0.1", tags); err != nil || v.String() != "v0.0.1" {
		t.Fatalf("exact without v: %v %v", v, err)
	}
	if _, err := Match("v0.0.9", tags); err == nil {
		t.Fatal("unknown version should fail")
	}
	if _, err := Match("main", tags); err == nil {
		t.Fatal("a branch ref must be rejected")
	}
}

func TestMajorOf(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"v0.1.0", 0},
		{"v1.0.0", 1},
		{"v1.12.4", 1},
		{"v2.0.0", 2},
		{"v2.3.4-rc.1", 2},
		{"v10.0.0", 10},
		{"", 0},
		{"latest", 0},
		{"2.0.0", 0},
		{"pkg/v2.0.0", 0},
		{"v2.0", 0},
		{"abcdef1234", 0},
	}
	for _, tc := range cases {
		if got := MajorOf(tc.in); got != tc.want {
			t.Errorf("MajorOf(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestLatestInMajor(t *testing.T) {
	tags := []string{
		"pkg/v2.9.9",
		"v0.1.0",
		"v0.2.0",
		"v1.0.0",
		"v1.12.4",
		"v1.2.0",
		"v2.0.0-rc.1",
		"v2.0.0",
		"v2.1.3",
		"v2.1.10",
		"v3.0.0-alpha.1",
	}

	cases := []struct {
		major  int
		want   string
		wantOK bool
	}{
		{0, "v0.2.0", true},
		{1, "v1.12.4", true},
		{2, "v2.1.10", true},
		{3, "v3.0.0-alpha.1", true},
		{4, "", false},
	}
	for _, tc := range cases {
		got, ok := LatestInMajor(tags, tc.major)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("LatestInMajor(major=%d) = (%q, %v), want (%q, %v)", tc.major, got, ok, tc.want, tc.wantOK)
		}
	}
}
