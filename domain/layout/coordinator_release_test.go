package layout

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// releasingRenderer gives every rendering unique artifact paths and records
// which ones the Coordinator released.
type releasingRenderer struct {
	*scriptedRenderer
	afterRender func()
	mu          sync.Mutex
	rendered    map[string]bool
	released    map[string]bool
}

func newReleasingRenderer() *releasingRenderer {
	return &releasingRenderer{scriptedRenderer: newScriptedRenderer(), rendered: map[string]bool{}, released: map[string]bool{}}
}

func (r *releasingRenderer) Render(ctx context.Context, req RenderRequest) (Rendering, error) {
	out, err := r.scriptedRenderer.Render(ctx, req)
	if err != nil {
		return out, err
	}
	r.mu.Lock()
	for i := range out.Artifacts {
		out.Artifacts[i].Path = fmt.Sprintf("/unit/%d/%s", req.Attempt, out.Artifacts[i].Kind)
		r.rendered[out.Artifacts[i].Path] = true
	}
	r.mu.Unlock()
	if r.afterRender != nil {
		r.afterRender()
	}
	return out, nil
}

func (r *releasingRenderer) Release(_ context.Context, artifacts []Artifact) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range artifacts {
		r.released[a.Path] = true
	}
	return nil
}

// live is every rendered artifact path not yet released.
func (r *releasingRenderer) live() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for p := range r.rendered {
		if !r.released[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func bestPaths(res Result) []string {
	out := []string{}
	if res.Best != nil && res.Best.Review != nil {
		for _, a := range res.Best.Review.Evidence.Artifacts {
			out = append(out, a.Path)
		}
	}
	sort.Strings(out)
	return out
}

type cancellingReviewer struct{ cancel context.CancelFunc }

func (r cancellingReviewer) Review(ctx context.Context, _ ReviewRequest) (Review, error) {
	r.cancel()
	<-ctx.Done()
	return Review{}, ctx.Err()
}

// PR #85 Must 5: only the returned best candidate's capture survives Run.
func TestRun_ReleasesEveryCaptureButTheBest(t *testing.T) {
	blocking := reviewStep{findings: []Finding{reviewFinding(CriterionAmbiguousCrossing)}}
	cases := map[string]struct {
		reviewer   func(cancel context.CancelFunc) Reviewer
		cancelPost bool
		wantBest   bool
		rejected   bool
	}{
		"budget exhausted: discarded losers": {reviewer: func(context.CancelFunc) Reviewer {
			return &scriptedReviewer{steps: map[int]reviewStep{1: blocking}, fallback: reviewStep{findings: []Finding{reviewFinding(CriterionAmbiguousCrossing), reviewFinding(CriterionMergedLines)}}}
		}, wantBest: true},
		"review error": {reviewer: func(context.CancelFunc) Reviewer {
			return &scriptedReviewer{steps: map[int]reviewStep{1: blocking, 2: {err: errors.New("boom")}}}
		}, wantBest: true},
		// A rejected rendering is released at once; the unreviewed candidate
		// may still be returned, but its evidence can never count.
		"rejected evidence": {reviewer: func(context.CancelFunc) Reviewer {
			return &scriptedReviewer{fallback: reviewStep{wrongDigest: true}}
		}, rejected: true},
		"cancel during review":           {reviewer: func(cancel context.CancelFunc) Reviewer { return cancellingReviewer{cancel} }},
		"render finishes as ctx expires": {reviewer: func(context.CancelFunc) Reviewer { return &scriptedReviewer{} }, cancelPost: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := newReleasingRenderer()
			if tc.cancelPost {
				r.afterRender = cancel
			}
			res := newCoord(t, r, tc.reviewer(cancel)).Run(ctx, prReviewJob(t, 5), nil)
			require.NotEmpty(t, r.rendered)
			if tc.rejected {
				require.True(t, res.Unreviewed)
				assert.Empty(t, r.live())
				return
			}
			assert.Equal(t, bestPaths(res), r.live())
			if tc.wantBest {
				assert.NotEmpty(t, r.live(), "the best candidate's evidence is kept for the worker to store")
			} else {
				assert.Empty(t, r.live())
			}
		})
	}
}
