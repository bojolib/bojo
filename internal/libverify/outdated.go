package libverify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bojolib/bojo/internal/cache"
	"github.com/bojolib/bojo/internal/tag"
)

// CheckOutdated queries remote tags for each declared `[pkg.deps-from]`
// dependency and compares them against the pinned version using `tag.Latest`.
// Outdated dependencies produce warnings by default, or errors when
// `opts.StrictOutdated` is true.
func CheckOutdated(opts Options, deps []DepRef) CheckResult {
	res := CheckResult{Name: "outdated"}
	if len(deps) == 0 {
		res.Status = StatusPass
		res.Summary = "no external dependencies"
		return res
	}

	fetchTags := opts.TagsFetcher
	if fetchTags == nil {
		store := cache.New(opts.Dir)
		fetchTags = store.Tags
	}

	sorted := append([]DepRef(nil), deps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Repo < sorted[j].Repo })

	sev := SeverityWarning
	if opts.StrictOutdated {
		sev = SeverityError
	}

	outdatedCount := 0
	for _, dep := range sorted {
		tags, err := fetchTags(dep.Repo)
		if err != nil {
			res.Issues = append(res.Issues, Issue{
				Check:    "outdated",
				Severity: sev,
				Message:  fmt.Sprintf("could not query tags for %s: %v", dep.Repo, err),
			})
			continue
		}
		latest, err := tag.Latest(tags)
		if err != nil {
			// No semver tags upstream; nothing to compare against.
			continue
		}

		if strings.TrimSpace(dep.Version) == "" {
			if dep.Commit != "" {
				res.Issues = append(res.Issues, Issue{
					Check:    "outdated",
					Severity: SeverityInfo,
					Message:  fmt.Sprintf("%s is pinned to commit %s (latest release %s)", dep.Repo, shortSHA(dep.Commit), latest),
				})
			}
			continue
		}

		curRaw := strings.TrimSpace(dep.Version)
		if !strings.HasPrefix(curRaw, "v") {
			curRaw = "v" + curRaw
		}
		cur, err := tag.ParseVersion(curRaw)
		if err != nil {
			res.Issues = append(res.Issues, Issue{
				Check:    "outdated",
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("%s has non-semver version %q (latest release %s)", dep.Repo, dep.Version, latest),
			})
			continue
		}

		if latest.Compare(cur) > 0 {
			outdatedCount++
			res.Issues = append(res.Issues, Issue{
				Check:    "outdated",
				Severity: sev,
				Message:  fmt.Sprintf("%s is pinned at %s; newer version %s is available", dep.Repo, cur, latest),
			})
		}
	}

	res.Status = statusFromIssues(res.Issues)
	if outdatedCount > 0 {
		res.Summary = fmt.Sprintf("%d of %d dependency(ies) have newer releases", outdatedCount, len(sorted))
	} else if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("%d dependency(ies) up to date", len(sorted))
	} else {
		res.Summary = "dependency tag check reported issues"
	}
	return res
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
