// Package config defines the application's persisted configuration and the
// import boundary for Octopus/AionUi account backups.
//
// Load and Save both pass data through the same normalization and validation
// rules, keeping Config as the single source of truth for defaults, URL cleanup,
// credential inference, and mutually exclusive credential fields. Import code
// converts external JSON shapes into that model and records unsupported or
// incomplete accounts instead of silently generating unusable sites.
package config
