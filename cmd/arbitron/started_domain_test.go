package main

import (
	"context"
	"errors"
	"net/http"
)

// errOfflineCatalog is the hermetic catalog leaf's answer: these stacks test
// execution, not model discovery, and must never reach the real catalog.
var errOfflineCatalog = errors.New("catalog offline in this test")

// initStartedDomain is initDomain followed by Start, the serving lifecycle that
// owns crash recovery, group/spawn reconciliation and the recovery sweeps. The
// catalog leaf is offline, so the startup model refresh only logs its failure.
// On a Start failure the stack is closed and the error returned.
func initStartedDomain(ctx context.Context, configPath string) (*domainStack, error) {
	ds, err := initDomainWithModelDependencies(ctx, configPath, ModelDependencies{
		FetchTransport: catalogHTTPLeaf(func(*http.Request) (*http.Response, error) { return nil, errOfflineCatalog }),
	})
	if err != nil {
		return nil, err
	}
	if err := ds.Start(ctx); err != nil {
		ds.Close(context.Background())
		return nil, err
	}
	return ds, nil
}

// groupTestProfilesYAML declares the default profiles the group fixtures name,
// on a fixed model version: profiles without one (like the shipped defaults'
// model "") are refused at seed, and group authoring validates every resolver
// and node profile against the seeded table.
const groupTestProfilesYAML = "profiles:\n" +
	"  - alias: \"default-chat\"\n    kind: \"chat\"\n    model: \"claude-sonnet-4-5\"\n" +
	"  - alias: \"default-agent\"\n    kind: \"agent\"\n    model: \"claude-sonnet-4-5\"\n"
