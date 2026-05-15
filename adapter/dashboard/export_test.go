package dashboard

import "io/fs"

// AssetsForTest exports assets() for use by external tests.
func AssetsForTest() fs.FS { return assets() }
