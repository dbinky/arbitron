package main

import (
	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
)

// wireLibrary builds the filesystem library store for the workspace and
// attaches it to the workflow service. The store is returned so other
// consumers (the sample seeder) share the one instance.
func wireLibrary(svc *workflow.Service, workspacePath string) workflow.LibraryStore {
	lib := filesystem.NewLibraryStore(workspacePath)
	svc.SetLibraryStore(lib)
	return lib
}
