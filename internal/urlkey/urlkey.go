// Package urlkey normalises repository URLs and derives stable cache keys.
//
// Normalisation is deliberately component-based (DESIGN §7, D14a): string
// replacement gets the scp form `git@host:owner/repo` wrong, because the `git@`
// looks like userinfo.
package urlkey

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Normalize returns the canonical form of a repository URL and rejects URLs
// that carry credentials (D25).
func Normalize(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("empty repository URL")
	}
	// A fragment is never part of the repository identity.
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}

	// scp-like form: git@host:owner/repo(.git)
	if !strings.Contains(s, "://") {
		if at := strings.LastIndex(s, "@"); at >= 0 {
			rest := s[at+1:]
			if colon := strings.Index(rest, ":"); colon >= 0 {
				host := rest[:colon]
				path := strings.TrimSuffix(rest[colon+1:], "/")
				s = "https://" + host + "/" + path
			}
		}
	}

	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid repository URL %q: %w", raw, err)
	}
	if u.User != nil {
		// `git@` is the conventional SSH user, not a credential; anything with a
		// password, or a different user on a plain HTTP(S) URL, is rejected (D25).
		_, hasPassword := u.User.Password()
		user := strings.ToLower(u.User.Username())
		if hasPassword || (user != "git" && user != "") {
			return "", fmt.Errorf("repository URL must not contain credentials")
		}
		u.User = nil
	}
	if u.Host == "" {
		return "", fmt.Errorf("repository URL %q has no host", raw)
	}

	// A repository path becomes a directory below the cache; no segment may be
	// empty, "." or ".." (D63).
	for _, seg := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		switch {
		case seg == "":
			continue // doubled slashes are cleaned by Normalize
		case seg == "." || seg == "..":
			return "", fmt.Errorf("repository URL %q has a forbidden path segment %q", raw, seg)
		}
	}

	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "git", "ssh":
		scheme = "https"
	case "http":
		scheme = "https"
	case "https":
	default:
		if scheme == "" {
			scheme = "https"
		} else {
			return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
		}
	}

	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "443" || port == "80" || port == "22" {
		port = ""
	}
	if strings.Contains(host, ":") { // IPv6
		host = "[" + host + "]"
	}
	if port != "" {
		host = host + ":" + port
	}

	path := u.Path
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimSuffix(path, ".GIT")
	path = strings.ReplaceAll(path, "//", "/")
	if path == "" {
		return "", fmt.Errorf("repository URL %q has no path", raw)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	return scheme + "://" + host + path, nil
}

// Hosts maps a well-known host to its single import-path segment (D82). The
// table is closed on purpose: an unknown host keeps its literal name instead of
// inventing an abbreviation that could later collide with another host.
var Hosts = map[string]string{
	"github.com":   "gh",
	"gitlab.com":   "gl",
	"codeberg.org": "cb",
	"git.sr.ht":    "sr",
}

// HostForPath returns the import-path segment for a host name.
func HostForPath(host string) string {
	h := strings.ToLower(host)
	if short, ok := Hosts[h]; ok {
		return short
	}
	return h
}

// Identifier turns a repository or owner name into a Mojo path segment (D83):
// a dash is not legal in a Mojo identifier, so it becomes an underscore.
func Identifier(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}

// RepoName returns the derived package name of a repository: its last path
// segment as a Mojo identifier.
func RepoName(raw string) (string, error) {
	norm, err := Normalize(raw)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(norm)
	if err != nil {
		return "", err
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segments) == 0 || segments[len(segments)-1] == "" {
		return "", fmt.Errorf("%q has no repository name", raw)
	}
	return Identifier(segments[len(segments)-1]), nil
}

// Hash returns the 12-hex cache key for a normalised URL.
// ModulePath is the import path of one package: <host>.<owner>.<repo>, where
// <host> is the short alias from Hosts (D82) and the repository name is the
// package name (D83).
func ModulePath(normalized string) (string, error) {
	u, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("%q has no host", normalized)
	}
	repo := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	if repo == "" {
		return "", fmt.Errorf("%q has no repository path", normalized)
	}
	segments := strings.Split(repo, "/")
	if len(segments) < 2 {
		return "", fmt.Errorf("%q needs an owner and a repository", normalized)
	}
	parts := []string{HostForPath(u.Host)}
	for _, s := range segments {
		parts = append(parts, Identifier(s))
	}
	return strings.Join(parts, "."), nil
}

// VersionedModulePath returns the collision-free import path for a repository
// at a given major version: `<host>.<owner>.<repo>` for v0/v1, and
// `v<N>.<host>.<owner>.<repo>` for major >= 2.
func VersionedModulePath(normalized string, major int) (string, error) {
	base, err := ModulePath(normalized)
	if err != nil {
		return "", err
	}
	if major >= 2 {
		return fmt.Sprintf("v%d.%s", major, base), nil
	}
	return base, nil
}

// ParseModulePath splits an optional `v<N>.` major-version prefix (N >= 2)
// from a module path. Paths without a v2+ prefix return (0, modPath).
func ParseModulePath(modPath string) (major int, base string) {
	dot := strings.IndexByte(modPath, '.')
	if dot <= 1 || dot == len(modPath)-1 || modPath[0] != 'v' {
		return 0, modPath
	}
	digits := modPath[1:dot]
	if digits[0] == '0' {
		return 0, modPath
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 2 {
		return 0, modPath
	}
	return n, modPath[dot+1:]
}

// SplitDepKey splits an optional `@v<N>` major-series suffix from a deps-from
// table key, allowing v1 and v2 of the same repository to coexist in TOML:
//
//	[deps-from."https://github.com/bojolib/socket"]
//	version = "v1.4.0"
//
//	[deps-from."https://github.com/bojolib/socket@v2"]
//	version = "v2.0.0"
//
// A plain URL without a `@v<N>` suffix returns (key, 0).
func SplitDepKey(key string) (repo string, major int) {
	at := strings.LastIndexByte(key, '@')
	if at <= 0 {
		return key, 0
	}
	suffix := key[at+1:]
	if len(suffix) < 2 || suffix[0] != 'v' {
		return key, 0
	}
	digits := suffix[1:]
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return key, 0
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return key, 0
	}
	return key[:at], n
}

// Path turns a normalised URL into a filesystem path without scheme, i.e.
// https://github.com/bojolib/socket -> github.com/bojolib/socket (D63).
func Path(normalized string) (string, error) {
	u, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("%q has no host", normalized)
	}
	p := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	if p == "" {
		return "", fmt.Errorf("%q has no repository path", normalized)
	}
	return u.Host + "/" + p, nil
}

func Hash(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])[:12]
}
