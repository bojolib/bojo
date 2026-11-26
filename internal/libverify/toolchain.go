package libverify

import (
	"fmt"

	"github.com/bojolib/bojo/internal/toolchain"
)

// CheckToolchain resolves the requested Mojo toolchain binary and verifies its
// compiler fingerprint. It returns the CheckResult, resolved binary path, and
// fingerprint string.
func CheckToolchain(opts Options) (CheckResult, string, string) {
	res := CheckResult{Name: "toolchain"}

	bin := opts.MojoBin
	if bin == "" {
		resolved, err := toolchain.Resolve(opts.MojoVersion)
		if err != nil {
			res.Status = StatusFail
			res.Summary = "mojo toolchain not available"
			res.Issues = append(res.Issues, Issue{
				Check:    "toolchain",
				Severity: SeverityError,
				Message:  err.Error(),
			})
			return res, "", ""
		}
		bin = resolved
	}

	fp, err := toolchain.Fingerprint(bin)
	if err != nil {
		res.Status = StatusFail
		res.Summary = "failed to query mojo version"
		res.Issues = append(res.Issues, Issue{
			Check:    "toolchain",
			Severity: SeverityError,
			Message:  fmt.Sprintf("fingerprinting %s: %v", bin, err),
		})
		return res, bin, ""
	}

	res.Status = StatusPass
	res.Summary = "mojo " + fp
	return res, bin, fp
}
