package engine

// ResolveNodeBinding exposes resolveNodeBinding to the external engine_test
// package, so seam tests can compare the displayed binding with what the real
// executors select.
var ResolveNodeBinding = resolveNodeBinding
