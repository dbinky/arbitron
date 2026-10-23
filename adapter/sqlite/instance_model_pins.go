package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
)

// encodeModelPins validates an instance's pin discriminator and returns the
// (model_pins, model_pins_version) column pair to write.
//
// Only version 1 snapshots may be created, always as non-NULL JSON (including
// "{}" for a workflow with no aliases). Legacy NULL/0 records are read-only
// compatibility for rows predating migration 024, never a new-run fallback.
func encodeModelPins(inst *instance.Instance) (sql.NullString, int, error) {
	switch inst.ModelPinsVersion {
	case instance.ModelPinsVersionLegacy:
		return sql.NullString{}, 0, fmt.Errorf("model pins: new instances require a version 1 snapshot; legacy version 0 is read-only")
	case instance.ModelPinsVersionSnapshot:
		if inst.ModelPins == nil {
			return sql.NullString{}, 0, fmt.Errorf("model pins: version 1 requires a non-nil snapshot (use an empty map for no aliases)")
		}
		if err := validateModelPins(inst.ModelPins); err != nil {
			return sql.NullString{}, 0, err
		}
		b, err := json.Marshal(inst.ModelPins)
		if err != nil {
			return sql.NullString{}, 0, fmt.Errorf("model pins: marshalling: %w", err)
		}
		return sql.NullString{String: string(b), Valid: true}, instance.ModelPinsVersionSnapshot, nil
	default:
		return sql.NullString{}, 0, fmt.Errorf("model pins: unsupported version %d", inst.ModelPinsVersion)
	}
}

// decodeModelPins reads snapshots and historical legacy rows. Only NULL with
// version 0 is legacy; every other inconsistency fails before any provider call
// instead of falling back to live resolution.
func decodeModelPins(raw sql.NullString, version int) (map[string]string, int, error) {
	switch version {
	case instance.ModelPinsVersionLegacy:
		if raw.Valid {
			return nil, 0, fmt.Errorf("model pins: payload present with legacy version 0")
		}
		return nil, instance.ModelPinsVersionLegacy, nil
	case instance.ModelPinsVersionSnapshot:
		if !raw.Valid {
			return nil, 0, fmt.Errorf("model pins: NULL payload with version 1")
		}
		var pins map[string]string
		if err := json.Unmarshal([]byte(raw.String), &pins); err != nil {
			return nil, 0, fmt.Errorf("model pins: invalid payload: %w", err)
		}
		if pins == nil { // the JSON literal null
			return nil, 0, fmt.Errorf("model pins: payload is JSON null")
		}
		if err := validateModelPins(pins); err != nil {
			return nil, 0, err
		}
		return pins, instance.ModelPinsVersionSnapshot, nil
	default:
		return nil, 0, fmt.Errorf("model pins: unsupported version %d", version)
	}
}

// decodeStoredModelPins preserves SQLite's storage type until validation:
// database/sql's integer conversion would accept a BLOB containing "0" as a
// legacy discriminator. Only an actual INTEGER 0/1 can identify a pin format.
func decodeStoredModelPins(raw sql.NullString, rawVersion any) (map[string]string, int, error) {
	version, ok := rawVersion.(int64)
	if !ok {
		return nil, 0, fmt.Errorf("model pins: non-integer version (%T)", rawVersion)
	}
	if version != instance.ModelPinsVersionLegacy && version != instance.ModelPinsVersionSnapshot {
		return nil, 0, fmt.Errorf("model pins: unsupported version %d", version)
	}
	return decodeModelPins(raw, int(version))
}

// validateModelPins rejects an empty alias or an empty pinned model: a pin that
// names no model cannot be the exact model a run executes on.
func validateModelPins(pins map[string]string) error {
	for alias, model := range pins {
		if alias == "" {
			return fmt.Errorf("model pins: empty profile alias")
		}
		if model == "" {
			return fmt.Errorf("model pins: alias %q pins an empty model", alias)
		}
	}
	return nil
}

// pinRouteRecord is one alias's stored D8 exception: the canonical family and
// the profile connection_ref captured at trigger. Rows written before the
// connection was recorded store the bare family string instead; the resolver
// refuses those aliases rather than trusting the live connection.
type pinRouteRecord struct {
	Family     string `json:"family"`
	Connection string `json:"connection"`
}

// encodeModelPinRoutes writes every new snapshot's exception map as non-NULL
// JSON. Capture owns route eligibility; storage enforces the record shape.
func encodeModelPinRoutes(inst *instance.Instance) (string, error) {
	if err := validateModelPinRoutes(inst.ModelPins, inst.ModelPinRoutes, inst.ModelPinRouteConnections, inst.ModelPinsVersion); err != nil {
		return "", err
	}
	records := make(map[string]any, len(inst.ModelPinRoutes))
	for alias, family := range inst.ModelPinRoutes {
		if conn := inst.ModelPinRouteConnections[alias]; conn != "" {
			records[alias] = pinRouteRecord{Family: family, Connection: conn}
		} else {
			records[alias] = family
		}
	}
	b, err := json.Marshal(records)
	if err != nil {
		return "", fmt.Errorf("model pin routes: marshalling: %w", err)
	}
	return string(b), nil
}

// decodeModelPinRoutes returns the alias→family exceptions and the
// alias→connection_ref captured at trigger (absent for older family-only rows).
func decodeModelPinRoutes(raw sql.NullString, pins map[string]string, version int) (routes, connections map[string]string, err error) {
	if !raw.Valid {
		return nil, nil, nil // Pre-031 rows have no exceptions; they remain strict.
	}
	var records map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw.String), &records); err != nil {
		return nil, nil, fmt.Errorf("model pin routes: invalid payload: %w", err)
	}
	if records == nil {
		return nil, nil, fmt.Errorf("model pin routes: payload is JSON null")
	}
	routes = make(map[string]string, len(records))
	for alias, rec := range records {
		var family string
		if json.Unmarshal(rec, &family) == nil {
			routes[alias] = family
			continue
		}
		var r pinRouteRecord
		dec := json.NewDecoder(bytes.NewReader(rec))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&r); err != nil || r.Connection == "" {
			return nil, nil, fmt.Errorf("model pin routes: invalid exception for alias %q", alias)
		}
		routes[alias] = r.Family
		if connections == nil {
			connections = make(map[string]string)
		}
		connections[alias] = r.Connection
	}
	if err := validateModelPinRoutes(pins, routes, connections, version); err != nil {
		return nil, nil, err
	}
	return routes, connections, nil
}

func validateModelPinRoutes(pins, routes, connections map[string]string, version int) error {
	if len(routes) != 0 && version != instance.ModelPinsVersionSnapshot {
		return fmt.Errorf("model pin routes: exceptions require a version 1 snapshot")
	}
	for alias, route := range routes {
		if alias == "" || pins[alias] == "" {
			return fmt.Errorf("model pin routes: exception requires a pinned alias")
		}
		if route != provider.CanonicalAzure && route != provider.CanonicalBedrock {
			return fmt.Errorf("model pin routes: unsupported native family")
		}
	}
	for alias, conn := range connections {
		if routes[alias] == "" || conn == "" {
			return fmt.Errorf("model pin routes: connection requires a native route exception")
		}
	}
	return nil
}

// pinRecordCorrupt reports whether row's stored model pin record (payload,
// version and route exceptions) cannot be decoded. Readers only report such
// rows; the engine owns failing them (instance.CorruptPinsError).
func pinRecordCorrupt(row generated.Instance) error {
	pins, version, err := decodeStoredModelPins(row.ModelPins, row.ModelPinsVersion)
	if err == nil {
		_, _, err = decodeModelPinRoutes(row.ModelPinRoutes, pins, version)
	}
	return err
}

// executionInstances backs the queue-position read (ListQueued). It is a pure
// read: a corrupt pin record is skipped and logged; queue admission
// (NextEligible) reports it for the engine to fail.
func executionInstances(_ context.Context, _ *sql.DB, rows []generated.Instance) ([]instance.Instance, error) {
	out := make([]instance.Instance, 0, len(rows))
	for _, row := range rows {
		if err := pinRecordCorrupt(row); err != nil {
			slog.Warn("skipping run with corrupt model pins", "instanceID", row.ID, "error", err)
			continue
		}
		inst, err := rowToInstance(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *inst)
	}
	return out, nil
}
