// Package checkin implements the complete single-site check-in workflow for
// NewAPI-compatible management panels.
//
// The package deliberately separates orchestration from transport details:
// RunWithOptions resolves credentials and user identity, checkinSite selects
// the compatible status/action path, and doRequest owns HTTP normalization and
// response decoding. Captcha and Turnstile solvers are injected through
// Options so interactive CLI behavior and unattended automation share the same
// business flow.
//
// A central safety rule is that a successful status query is not a successful
// check-in action. Action responses must contain reward evidence, an explicit
// success message, or a follow-up status confirming that the account is checked
// in today. This prevents common NewAPI variants from producing false-positive
// results for payloads such as {"success":true,"message":"查询成功"}.
package checkin
