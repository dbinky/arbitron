package dashboard

import "io/fs"

// AssetsForTest exports the embedded dist FS for the existing handler tests.
func AssetsForTest() fs.FS { return Assets() }
