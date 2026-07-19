package checkin

import "time"

// Result is the complete observable outcome of a single site attempt. Pointer
// amounts distinguish an unavailable value from a legitimate zero reward or
// zero balance; BalanceError is kept separate because balance lookup is useful
// but does not change the success of an already confirmed check-in.
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

// SitePlatform identifiers supported by this tool. They currently share the
// NewAPI-compatible request flow but remain explicit for platform-specific
// response handling such as OneAPI's used_quota subtraction.
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

// supportsCheckin is the early allow-list used before any network request.
func supportsCheckin(platform string) bool {
	switch platform {
	case PlatformNewAPI, PlatformAnyRouter, PlatformOneAPI, PlatformVeloera, PlatformDoneHub, PlatformNewAPILike:
		return true
	default:
		return false
	}
}

// isNewAPILikePlatform documents the compatibility family independently from
// the public support check; callers can use it for family-wide behavior.
func isNewAPILikePlatform(platform string) bool {
	switch platform {
	case PlatformNewAPI, PlatformOneAPI, PlatformVeloera, PlatformDoneHub, PlatformNewAPILike, PlatformAnyRouter:
		return true
	default:
		return false
	}
}
