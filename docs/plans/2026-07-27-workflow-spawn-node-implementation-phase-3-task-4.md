# Phase 3 — Task 4: composition-root wiring for the spawn node

**Goal:** Construct the child-input preparer and the spawn executor in `cmd/arbitron/main.go`, register the executor into the live executors map, and rename `scheduleIdentityResolver` to a neutral name now that the same value serves both the schedule evaluator and the spawn executor.

**Architecture:** The executor is registered **after** `engine.NewEngine`, in the same block as `timer_delay` and `webhook_wait`, because it depends on the engine itself (`*Engine` satisfies `ChildSpawner`) — the engine stores a *reference* to the executors map, not a copy, so post-construction mutation is the sanctioned way to close this dependency cycle. The identity resolver is constructed **once** and passed to the schedule evaluator, the schedule runner, and the spawn executor, so a scheduled fire and a spawned run can never resolve a user differently.

**Tech Stack:** Go 1.25.5, Cobra, standard library `testing`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 4: wire the preparer and executor at the composition root

**Files:**
- Modify: `cmd/arbitron/main.go:1297-1332` (rename `scheduleIdentityResolver` → `userIdentityResolver`)
- Modify: `cmd/arbitron/main.go:863-882` (pass the single resolver value)
- Modify: `cmd/arbitron/main.go:805-816` (register the spawn executor after engine construction)
- Test:   `cmd/arbitron/spawn_wiring_test.go`

---

- [x] **Step 1: Write the failing test**

Create `cmd/arbitron/spawn_wiring_test.go`:

```go
package main

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ONE concrete resolver must satisfy BOTH domain ports, so the composition
// root can pass the same value to the schedule evaluator and the spawn
// executor and they can never resolve a user differently.
var _ engine.IdentityResolver = userIdentityResolver{}

// stubUserRepo is the minimal instance.UserRepository the resolver needs.
type stubUserRepo struct {
	instance.UserRepository
	users map[uuid.UUID]*instance.User
	depts map[uuid.UUID][]string
}

func (r stubUserRepo) GetByID(_ context.Context, id uuid.UUID) (*instance.User, error) {
	u, ok := r.users[id]
	if !ok {
		return nil, instance.ErrNotFound
	}
	return u, nil
}

func (r stubUserRepo) ListDepartments(_ context.Context, id uuid.UUID) ([]string, error) {
	return r.depts[id], nil
}

// The local-mode default user (seeded by migration 001 as the admin "local")
// resolves cleanly through the SAME builder the request path uses. This is the
// precondition the spawn wiring depends on: without it, every spawn in a
// local-mode deployment would fail at identity reconstruction.
func TestUserIdentityResolver_LocalDefaultUser(t *testing.T) {
	localID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	repo := stubUserRepo{
		users: map[uuid.UUID]*instance.User{
			localID: {ID: localID, Username: "local", Role: "admin"},
		},
		depts: map[uuid.UUID][]string{},
	}
	r := userIdentityResolver{users: repo, groups: identity.GroupConfig{DefaultGroup: "default"}}

	got, err := r.IdentityForUser(context.Background(), localID)
	require.NoError(t, err, "the local-mode default user must resolve; every spawn depends on it")
	assert.Equal(t, localID, got.UserID)
	assert.Equal(t, "admin", got.Role)
	assert.Equal(t, []string{"default"}, got.Departments,
		"a user with no stored departments still belongs to the default group")
}

func TestUserIdentityResolver_CarriesStoredDepartments(t *testing.T) {
	uid := uuid.New()
	repo := stubUserRepo{
		users: map[uuid.UUID]*instance.User{uid: {ID: uid, Username: "dev", Role: "user"}},
		depts: map[uuid.UUID][]string{uid: {"engineering", "platform"}},
	}
	r := userIdentityResolver{users: repo, groups: identity.GroupConfig{DefaultGroup: "default"}}

	got, err := r.IdentityForUser(context.Background(), uid)
	require.NoError(t, err)
	assert.Equal(t, []string{"engineering", "platform"}, got.Departments,
		"the reconstructed identity must carry the owner's departments so a "+
			"department-scoped child workflow resolves")
}

func TestUserIdentityResolver_UnknownUserErrors(t *testing.T) {
	r := userIdentityResolver{
		users:  stubUserRepo{users: map[uuid.UUID]*instance.User{}, depts: map[uuid.UUID][]string{}},
		groups: identity.GroupConfig{DefaultGroup: "default"},
	}
	_, err := r.IdentityForUser(context.Background(), uuid.New())
	require.Error(t, err, "identity resolution must fail rather than degrade to a system identity")
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./cmd/arbitron/ -run TestUserIdentityResolver
```

Expected output:

```
# github.com/arbitron/arbitron/cmd/arbitron [github.com/arbitron/arbitron/cmd/arbitron.test]
cmd/arbitron/spawn_wiring_test.go:16:33: undefined: userIdentityResolver
FAIL	github.com/arbitron/arbitron/cmd/arbitron [build failed]
```

- [x] **Step 3: Rename the resolver to a neutral name**

In `cmd/arbitron/main.go`, replace the type declaration and its comment
(lines 1297-1332):

```go
// userIdentityResolver builds a user's full runtime identity from a bare user
// id, for injection into a background context.
//
// It constructs the Identity via the SAME shared domain builder as
// AuthMiddleware.enrich (fed by the same UserRepository + GroupConfig ports),
// so a scheduled fire, a spawned child run, and the same user's interactive
// request all resolve to identical identities — LedDepartments included.
//
// The name is deliberately NOT schedule-specific: ONE value of this type is
// constructed at the composition root and passed to the schedule evaluator, the
// schedule run-now runner, AND the spawn executor. It satisfies both
// schedule.IdentityResolver and engine.IdentityResolver, which are structurally
// identical twins redeclared across a domain edge.
type userIdentityResolver struct {
	users  instance.UserRepository
	groups identity.GroupConfig
}

func (r userIdentityResolver) IdentityForUser(ctx context.Context, userID uuid.UUID) (identity.Identity, error) {
	u, err := r.users.GetByID(ctx, userID)
	if err != nil {
		return identity.Identity{}, err
	}
	depts, err := r.users.ListDepartments(ctx, userID)
	if err != nil {
		return identity.Identity{}, err
	}
	subject := ""
	if u.Subject != nil {
		subject = *u.Subject
	}
	email := ""
	if u.Email != nil {
		email = *u.Email
	}
	return identity.BuildIdentity(identity.UserIdentityInput{
		UserID:            u.ID,
		Username:          u.Username,
		Role:              u.Role,
		Subject:           subject,
		Email:             email,
		StoredDepartments: depts,
	}, r.groups), nil
}
```

- [x] **Step 4: Construct the resolver once and reuse it**

In `cmd/arbitron/main.go`, immediately before the `scheduleRepo := ...` line
(~line 862), insert:

```go
	// ONE identity resolver serves the schedule evaluator, the schedule run-now
	// runner, AND the spawn executor, so a scheduled fire and a spawned child
	// can never resolve the same user differently.
	identityResolver := userIdentityResolver{users: userRepo, groups: groupCfg}
```

Then replace both occurrences of
`scheduleIdentityResolver{users: userRepo, groups: groupCfg},` (lines 867 and
880) with:

```go
		identityResolver,
```

- [x] **Step 5: Construct and register the spawn executor**

The spawn executor depends on `eng` (which satisfies `engine.ChildSpawner`), so
it is registered AFTER engine construction, in the same block as `timer_delay`
and `webhook_wait`. But `identityResolver` is only built at step 4 (~line 862),
which is *after* that block. Move the executor registration to just after the
`identityResolver` line so both are in scope.

Insert immediately after the `identityResolver := ...` line added in Step 4:

```go
	// 12c-spawn. Wire the spawn_workflow executor.
	//
	// It is registered AFTER engine construction, exactly as timer_delay and
	// webhook_wait are, because it depends on the engine itself (*Engine
	// satisfies engine.ChildSpawner). This works because the engine stores a
	// REFERENCE to the executors map, not a copy, so this mutation is visible to
	// the engine at execution time.
	//
	// The preparer's call is tool-SUPPRESSED: it routes through the same
	// providerFactory chat arm llm_call uses, and the Phase-1 kind guard fails
	// closed if the node's model_profile resolves to an agent binding.
	childInputPreparer := engine.NewChildInputPreparer(providerFactory, profileResolver)
	executors[workflow.NodeTypeSpawnWorkflow] = engine.NewSpawnWorkflowExecutor(
		instanceRepo,
		childInputPreparer,
		eng,
		identityResolver,
		fileReader,
	)
```

- [x] **Step 6: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w cmd/arbitron/
go build ./... && go test ./cmd/arbitron/ -run TestUserIdentityResolver -v
```

Expected output:

```
--- PASS: TestUserIdentityResolver_LocalDefaultUser (0.00s)
--- PASS: TestUserIdentityResolver_CarriesStoredDepartments (0.00s)
--- PASS: TestUserIdentityResolver_UnknownUserErrors (0.00s)
PASS
ok  	github.com/arbitron/arbitron/cmd/arbitron	0.4s
```

- [x] **Step 7: Confirm the old name is gone**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
grep -rn "scheduleIdentityResolver" cmd/ || echo "OK: renamed everywhere"
```

Expected output: `OK: renamed everywhere`

- [x] **Step 8: Full Go gate**

```bash
go test ./...
```

Expected: `ok` for every package, no `FAIL` lines.

- [x] **Step 9: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add cmd/arbitron/main.go cmd/arbitron/spawn_wiring_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(cmd): wire the spawn_workflow executor and share one identity resolver"
```
