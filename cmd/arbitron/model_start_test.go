package main

import (
	"context"
	"fmt"
	"net/http"
)

// Recovery fixtures exercise the same public Start as serving, with a definite
// catalog outage at the HTTP leaf rather than contacting the host's network.
func initRecoveryTestDomain(ctx context.Context, path string) (*domainStack, error) {
	ds, err := initDomainWithModelDependencies(ctx, path, ModelDependencies{FetchTransport: catalogHTTPLeaf(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("restart fixture catalog outage") })})
	if err != nil {
		return nil, err
	}
	if err := ds.Start(ctx); err != nil {
		ds.Close(context.Background())
		return nil, err
	}
	return ds, nil
}
