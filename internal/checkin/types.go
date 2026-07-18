package checkin

import "time"

// Result is the outcome of a single site check-in attempt.
type Result struct {
	Site            string
	CheckedAt       time.Time
	Success         bool
	Message         string
	Reward          string
	RewardUSD       *float64
	TotalBalanceUSD *float64
	BalanceError    string
	Error           string
}

// SitePlatform identifiers supported by this tool.
const (
	PlatformNewAPI     = "new-api"
	PlatformAnyRouter  = "any-router"
	PlatformOneAPI     = "one-api"
	PlatformVeloera    = "veloera"
	PlatformDoneHub    = "done-hub"
	PlatformNewAPILike = "new-api-like"
)

type authCredential struct {
	Type  string
	Value string
}

func supportsCheckin(platform string) bool {
	switch platform {
	case PlatformNewAPI, PlatformAnyRouter, PlatformOneAPI, PlatformVeloera, PlatformDoneHub, PlatformNewAPILike:
		return true
	default:
		return false
	}
}

func isNewAPILikePlatform(platform string) bool {
	switch platform {
	case PlatformNewAPI, PlatformOneAPI, PlatformVeloera, PlatformDoneHub, PlatformNewAPILike, PlatformAnyRouter:
		return true
	default:
		return false
	}
}
