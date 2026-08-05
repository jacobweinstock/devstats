package contrib

import "strings"

// botLogins mirrors devstats' bot exclusions plus tinkerbell-specific bots.
// Anything ending in [bot] or -bot is also treated as a bot.
var botLogins = map[string]struct{}{
	"dependabot":                    {},
	"dependabot-preview":            {},
	"renovate":                      {},
	"renovate-bot":                  {},
	"github-actions":                {},
	"codecov":                       {},
	"codecov-io":                    {},
	"codecov-commenter":             {},
	"mergify":                       {},
	"mergify-bot":                   {},
	"tinkerbell-ci":                 {},
	"tinkerbell-bot":                {},
	"k8s-ci-robot":                  {},
	"stale":                         {},
	"imgbot":                        {},
	"pre-commit-ci":                 {},
	"allcontributors":               {},
	"copilot":                       {},
	"copilot-pull-request-reviewer": {},
}

// IsBot reports whether a login is a bot account. An empty login is treated
// as a bot so unattributed activity is dropped.
func IsBot(login string) bool {
	if login == "" {
		return true
	}
	low := strings.ToLower(login)
	if strings.HasSuffix(low, "[bot]") || strings.HasSuffix(low, "-bot") {
		return true
	}
	_, ok := botLogins[low]
	return ok
}
