package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
)

// pinPreflightNamespace keys deterministic notification IDs, so an unchanged
// refusal raises one notification across reboots rather than one per boot.
var pinPreflightNamespace = uuid.MustParse("6f1c2b0e-5a43-4d8e-9c1f-0b7a2e4d9c31")

type pinPreflighter interface {
	PinPreflight(context.Context) (refusals, unchecked []provider.PinRefusal, err error)
}

// reportPinPreflight runs after seeding at every boot. It turns "the first
// trigger after upgrade fails" into a boot WARN plus a notification naming each
// fixed-mode profile that will refuse triggers and why. It reads only local
// state and never fails startup: external (Teams) delivery runs on background,
// which Close waits for, so a slow webhook cannot hold readiness.
func reportPinPreflight(ctx context.Context, profiles pinPreflighter, notifs notification.Repository, external notification.ExternalNotifier, background *sync.WaitGroup, logger *slog.Logger) {
	refusals, unchecked, err := profiles.PinPreflight(ctx)
	if err != nil {
		if ctx.Err() == nil {
			logger.Warn("profile pin preflight failed", "error", err)
		}
		return
	}
	for _, u := range unchecked {
		logger.Warn("profile pin preflight could not check profile", "alias", u.Alias, "error", u.Err)
	}
	if len(refusals) == 0 {
		return
	}
	aliases := make([]string, 0, len(refusals))
	for _, r := range refusals {
		aliases = append(aliases, r.Alias)
		logger.Warn("profile will refuse triggers: set it to a fixed model version",
			"alias", r.Alias, "model", r.Model, "reason", r.Err)
	}
	logger.Warn("profiles will refuse triggers", "count", len(refusals), "aliases", aliases)
	if notifs == nil {
		return
	}
	for _, r := range refusals {
		n := &notification.Notification{
			ID:           uuid.NewSHA1(pinPreflightNamespace, []byte(r.Alias+"\x00"+r.Model+"\x00"+r.Err.Error())),
			WorkflowName: "profile " + r.Alias,
			Type:         notification.TypeProfileRefusesTriggers,
			CreatedAt:    time.Now(),
		}
		if err := notifs.Create(ctx, n); err != nil {
			if !errors.Is(err, notification.ErrAlreadyExists) {
				logger.Warn("profile pin preflight notification failed", "alias", r.Alias, "error", err)
			}
			continue
		}
		if external != nil {
			alias := r.Alias
			background.Go(func() {
				if err := external.Notify(ctx, n); err != nil && ctx.Err() == nil {
					logger.Warn("profile pin preflight external notification failed", "alias", alias, "error", err)
				}
			})
		}
	}
}
