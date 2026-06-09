package identity

import (
	"context"
	"fmt"
	"log/slog"
)

// DepartmentRenamer rewrites every stored occurrence of a department string from
// one value to another, across ALL tables that persist a department string (the
// membership snapshot, run stamps, schedule attribution, and department-visibility
// publications). It is a domain port; the SQLite adapter implements it. RenameDepartment
// MUST be idempotent: once the stored values are already the target, a re-run
// matches nothing and renames zero rows. It returns the total rows changed.
type DepartmentRenamer interface {
	RenameDepartment(ctx context.Context, from, to string) (rows int64, err error)
}

// ReconcileMappings rewrites historical department strings so a newly-mapped
// department is seen under its friendly name everywhere. It walks cfg.Mapping and,
// for each entry whose normalized raw key differs from its normalized friendly
// value, calls renamer.RenameDepartment(from, to). Entries that normalize to a
// blank key or value are skipped, as are identity renames (from == to). The pass
// is idempotent (the renamer's WHERE matches nothing once values are friendly), so
// it is safe to run on every boot. The FIRST renamer error aborts and is returned
// so the caller fails the boot loudly (fail-fast); the per-entry transaction in
// the adapter prevents a partial rename within an entry.
func ReconcileMappings(ctx context.Context, cfg GroupConfig, renamer DepartmentRenamer, log *slog.Logger) error {
	for rawKey, friendly := range cfg.Mapping {
		from := NormalizeDept(rawKey)
		to := NormalizeDept(friendly)
		if from == "" || to == "" || from == to {
			continue
		}
		rows, err := renamer.RenameDepartment(ctx, from, to)
		if err != nil {
			return fmt.Errorf("reconcile mapping %q -> %q: %w", from, to, err)
		}
		if rows > 0 && log != nil {
			log.Info("reconciled department mapping", "from", from, "to", to, "rows", rows)
		}
	}
	return nil
}
