package urlkey

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{
			name: "plain https",
			in:   "https://github.com/Owner/Repo",
			want: "https://github.com/Owner/Repo",
		},
		{
			name: "trailing slash and .git",
			in:   "https://github.com/Owner/Repo.git/",
			want: "https://github.com/Owner/Repo",
		},
		{
			name: "uppercase host and scheme, path stays",
			in:   "HTTPS://GitHub.COM/Owner/Repo",
			want: "https://github.com/Owner/Repo",
		},
		{
			name: "scp form",
			in:   "git@github.com:Owner/Repo.git",
			want: "https://github.com/Owner/Repo",
		},
		{
			name: "ssh url with default port",
			in:   "ssh://git@github.com:22/Owner/Repo",
			want: "https://github.com/Owner/Repo",
		},
		{
			name: "http is upgraded",
			in:   "http://example.com/a/b",
			want: "https://example.com/a/b",
		},
		{
			name: "nested gitlab subgroup",
			in:   "https://gitlab.com/group/sub/proj",
			want: "https://gitlab.com/group/sub/proj",
		},
		{
			name: "fragment and query removed",
			in:   "https://github.com/Owner/Repo?ref=main#addition",
			want: "https://github.com/Owner/Repo",
		},
		{
			name: "doubled slashes in path",
			in:   "https://github.com//Owner//Repo",
			want: "https://github.com/Owner/Repo",
		},
		{
			name: "uppercase dot git",
			in:   "https://github.com/Owner/Repo.GIT",
			want: "https://github.com/Owner/Repo",
		},
		{
			name:    "credentials rejected",
			in:      "https://user:token@github.com/Owner/Repo",
			wantErr: true,
		},
		{
			name:    "empty",
			in:      "   ",
			wantErr: true,
		},
		{
			name:    "no host",
			in:      "github.com/Owner/Repo",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Normalize(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize(%q) returned error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestHashIsStableAndTwelveHex(t *testing.T) {
	a := Hash("https://github.com/Owner/Repo")
	b := Hash("https://github.com/Owner/Repo")
	if a != b {
		t.Fatalf("Hash is not stable: %q vs %q", a, b)
	}
	if len(a) != 12 {
		t.Fatalf("Hash length = %d, want 12", len(a))
	}
	if Hash("https://github.com/Owner/Repo2") == a {
		t.Fatal("different URLs produced the same hash")
	}
}

func TestModulePath(t *testing.T) {
	cases := []struct{ repo, want string }{
		{"https://github.com/LukasLow/bojo_demo_pkg", "gh.LukasLow.bojo_demo_pkg"},
		{"https://github.com/LukasLow/bojo_demo_pkg.git", "gh.LukasLow.bojo_demo_pkg"},
		{"https://gitlab.com/a/b/c", "gl.a.b.c"},
		{"https://codeberg.org/lukas/mojo-tls", "cb.lukas.mojo_tls"},
		// An unknown host keeps its literal name: no invented abbreviation (D82).
		{"https://git.example.com/someone/repo", "git.example.com.someone.repo"},
	}
	for _, c := range cases {
		norm, err := Normalize(c.repo)
		if err != nil {
			t.Fatalf("%s: %v", c.repo, err)
		}
		got, err := ModulePath(norm)
		if err != nil {
			t.Fatalf("%s: %v", c.repo, err)
		}
		if got != c.want {
			t.Errorf("ModulePath(%s) = %q, want %q", c.repo, got, c.want)
		}
	}
}

func TestHostForPath(t *testing.T) {
	cases := map[string]string{
		"github.com":         "gh",
		"GitHub.COM":         "gh",
		"gitlab.com":         "gl",
		"codeberg.org":       "cb",
		"git.sr.ht":          "sr",
		"git.example.com":    "git.example.com",
		"gitlab.example.com": "gitlab.example.com",
	}
	for host, want := range cases {
		if got := HostForPath(host); got != want {
			t.Errorf("HostForPath(%q) = %q, want %q", host, got, want)
		}
	}
}
