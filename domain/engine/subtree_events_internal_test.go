package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/instance"
)

type stbBus struct{}

func (stbBus) Publish(uuid.UUID, Event) {}
func (stbBus) Subscribe(uuid.UUID) (<-chan Event, func()) {
	c := make(chan Event)
	close(c)
	return c, func() {}
}
func (stbBus) SubscribeAll() (<-chan ScopedEvent, func()) {
	c := make(chan ScopedEvent)
	close(c)
	return c, func() {}
}

type stbViewer struct{}

func (stbViewer) GetInstance(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	return &instance.Instance{ID: id}, nil
}

type stbRepo struct {
	instance.InstanceRepository
	mu   sync.Mutex
	rows map[uuid.UUID]*instance.Instance
	errs map[uuid.UUID]error
	gets map[uuid.UUID]int
}

func (r *stbRepo) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gets[id]++
	if err := r.errs[id]; err != nil {
		return nil, err
	}
	inst, ok := r.rows[id]
	if !ok {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	cp := *inst
	return &cp, nil
}

func newSTBRepo() *stbRepo {
	return &stbRepo{rows: map[uuid.UUID]*instance.Instance{}, errs: map[uuid.UUID]error{}, gets: map[uuid.UUID]int{}}
}

func TestSubtreeBridge_AncestryCacheIsBounded(t *testing.T) {
	repo := newSTBRepo()
	b := NewSubtreeEventBridge(stbBus{}, stbViewer{}, repo)
	b.maxAncestry = 3
	root := uuid.New()
	ids := make([]uuid.UUID, 5)
	for i := range ids {
		ids[i] = uuid.New()
		repo.rows[ids[i]] = &instance.Instance{ID: ids[i]}
	}
	ctx := context.Background()
	for _, id := range ids {
		assert.False(t, b.InSubtree(ctx, root, id))
	}

	assert.Equal(t, 3, b.ancestryLen(), "the cache never exceeds its bound")
	b.InSubtree(ctx, root, ids[4])
	assert.Equal(t, 1, repo.gets[ids[4]], "the most recent entry is still cached")
	b.InSubtree(ctx, root, ids[0])
	assert.Equal(t, 2, repo.gets[ids[0]], "the least recently used entry was evicted")
}

func TestSubtreeBridge_TransientStorageErrorIsNotCached(t *testing.T) {
	repo := newSTBRepo()
	b := NewSubtreeEventBridge(stbBus{}, stbViewer{}, repo)
	root := uuid.New()
	child := uuid.New()
	repo.rows[root] = &instance.Instance{ID: root}
	repo.rows[child] = &instance.Instance{ID: child, ParentInstanceID: &root}
	repo.errs[child] = errors.New("database is locked")
	ctx := context.Background()

	assert.False(t, b.InSubtree(ctx, root, child), "an unreadable chain degrades to not-in-subtree")
	assert.Equal(t, 0, b.ancestryLen(), "a storage error is not remembered")

	delete(repo.errs, child)
	assert.True(t, b.InSubtree(ctx, root, child), "the next lookup reads storage again")
	assert.Equal(t, 2, repo.gets[child])
}

// A row that is not readable yet must not be remembered as missing: the queue
// evaluator can start a committed row before instance.created is published,
// so the next lookup has to read storage again.
func TestSubtreeBridge_MissingRowIsNotCached(t *testing.T) {
	repo := newSTBRepo()
	b := NewSubtreeEventBridge(stbBus{}, stbViewer{}, repo)
	root, child := uuid.New(), uuid.New()
	ctx := context.Background()

	assert.False(t, b.InSubtree(ctx, root, child), "no row yet: not in subtree")
	assert.Equal(t, 0, b.ancestryLen(), "a missing row is not remembered")

	repo.rows[child] = &instance.Instance{ID: child, ParentInstanceID: &root}
	assert.True(t, b.InSubtree(ctx, root, child), "the committed row is found without any announcement")
	assert.Equal(t, 2, repo.gets[child])
	b.InSubtree(ctx, root, child)
	assert.Equal(t, 2, repo.gets[child], "a found row is cached")
}
