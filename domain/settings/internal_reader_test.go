package settings_test

import "github.com/arbitron/arbitron/domain/settings"

// The refresh and delivery runtime consume raw persisted settings, never the
// authorized redacted public projection.
var _ settings.InternalReader = (*settings.Service)(nil)
