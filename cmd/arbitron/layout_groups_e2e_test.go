package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise each authoring transport against the same immutable, reviewed sinks.
func TestLayoutGroups_RealRootSavedRevisionReachesReviewedSink(t *testing.T) {
	browser := requireBrowserE2E(t)
	binary := buildLayoutStdioBinary(t)
	for _, form := range []string{"branches", "workflows", "nested"} {
		for _, transport := range []string{"service", "http", "stdio"} {
			t.Run(form+"/"+transport, func(t *testing.T) {
				env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
				useLayoutE2EBrowser(t, browser)
				t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
				ds, _ := env.boot(t)
				ctx := adminCtxForLocalUser(t, ds)
				child := layoutE2EWorkflow("ownership-child", "Child")
				child.Inputs = []workflow.InputField{{Name: "message", Type: "string"}}
				require.NoError(t, ds.workflowSvc.Create(ctx, child.Name, child))
				requireGroupReviewCheckpoint(t, waitGroupGeometryCheckpoint(t, ds, layoutE2ESavedRevision(t, env.privatePath(child.Name))))
				router := newLayoutE2ERouter(t, ds)
				var process *layoutStdioProcess
				if transport == "stdio" {
					ds.layout.stop() // Only the shipped subprocess owns the worker now.
					process = startLayoutStdioWithin(t, binary, env, 5*time.Minute, "mcp")
					process.initialize(t)
				}
				wf := layoutOwnershipWorkflow(workflow.GroupFormBranches, form == "nested")
				if form == "workflows" {
					wf = layoutOwnershipWorkflow(workflow.GroupFormWorkflows, false)
				}
				encode := func(v any) string {
					b, err := json.Marshal(v)
					require.NoError(t, err)
					return string(b)
				}
				write := func(create bool, expected *workflow.Workflow) {
					t.Helper()
					switch transport {
					case "service":
						if create {
							require.NoError(t, ds.workflowSvc.Create(ctx, wf.Name, expected))
						} else {
							require.NoError(t, ds.workflowSvc.Update(ctx, wf.Name, expected))
						}
					case "http":
						method, path, want := http.MethodPut, "/api/v1/workflows/"+wf.Name, http.StatusOK
						if create {
							method, path, want = http.MethodPost, "/api/v1/workflows/", http.StatusCreated
						}
						status, body := layoutE2EDo(t, router, method, path, encode(expected))
						require.Equal(t, want, status, "%v", body)
					case "stdio":
						tool := "workflow_update"
						if create {
							tool = "workflow_create"
						}
						res := process.call(t, tool, tool, map[string]any{"name": wf.Name, "manifest": encode(expected)})
						require.False(t, res.IsError, mcpE2EText(t, res))
					}
				}
				var revisions []string
				checkpoint := func(expected *workflow.Workflow, grouped bool) layout.View {
					t.Helper()
					saved := layoutE2EManifest(t, env.privatePath(wf.Name))
					require.Equal(t, expected.Nodes, saved.Nodes)
					require.Equal(t, expected.Edges, saved.Edges)
					require.NotContains(t, revisions, saved.SavedRevision())
					revisions = append(revisions, saved.SavedRevision())
					before, err := os.ReadFile(env.privatePath(wf.Name))
					require.NoError(t, err)
					rec := waitGroupGeometryCheckpoint(t, ds, saved.SavedRevision())
					requireGroupReviewCheckpoint(t, rec)
					require.Equal(t, 5, rec.AttemptLimit)
					require.Positive(t, rec.AttemptsUsed)
					require.LessOrEqual(t, rec.AttemptsUsed, 5)
					var captured []byte
					require.NoError(t, ds.db.QueryRow("SELECT input_manifest FROM layout_revisions WHERE revision=?", saved.SavedRevision()).Scan(&captured))
					require.Equal(t, []byte(encode(saved)), captured, "exact captured bytes, including config/task order and retained presentation")
					require.Equal(t, captured, rec.Manifest)
					graph, err := layout.BuildGraph(saved)
					require.NoError(t, err)
					stored, err := ds.layout.store.Result(ctx, rec.Revision, rec.ResultGeneration)
					require.NoError(t, err)
					require.NoError(t, stored.Geometry.Validate(graph))
					require.True(t, layout.HasRenderedEvidence(stored.Geometry, stored.Review))
					require.Len(t, stored.Geometry.Edges, len(saved.Edges))
					for ordinal, edge := range saved.Edges {
						route := stored.Geometry.Edges[ordinal]
						require.Equal(t, ordinal, route.Ordinal)
						require.Equal(t, edge.Source, route.Source)
						require.Equal(t, edge.Target, route.Target)
						require.Equal(t, edge.Output, route.Output)
						require.Equal(t, edge.Label, route.Label)
						require.NotEmpty(t, route.SourceAttach.ElementID)
						require.NotEmpty(t, route.TargetAttach.ElementID)
					}
					if grouped {
						require.NotEmpty(t, graph.Groups.Order)
						require.NotEmpty(t, stored.Geometry.GroupElements)
						require.NotEmpty(t, stored.Geometry.GroupLanes)
						require.NotEmpty(t, stored.Geometry.GroupConnectors)
					}
					assertEvidenceOnDisk(t, env, stored.Review.Evidence)
					view := surfaceView(t, router, "/api/v1/workflows/"+wf.Name+"/layout")
					require.Equal(t, layout.DisplayComplete, view.DisplayStatus)
					require.Equal(t, stored.Geometry, *view.Geometry)
					require.Equal(t, stored.Generation, view.ResultGeneration)
					require.Equal(t, graph.Groups, view.Groups)
					after, err := os.ReadFile(env.privatePath(wf.Name))
					require.NoError(t, err)
					require.Equal(t, before, after)
					return view
				}
				write(true, wf)
				// Capture while pending: Trigger must freeze the committed authoring
				// revision, never a derived arranged graph or a later update.
				first := layoutE2EManifest(t, env.privatePath(wf.Name))
				id := surfaceSnapshot(t, ds, first)
				pending, err := ds.layout.store.Get(ctx, first.SavedRevision())
				require.NoError(t, err)
				require.False(t, pending.State.Terminal(), "the real queue boundary must freeze a still-pending authoring revision")
				originalView := checkpoint(wf, true)
				group := &wf.Nodes[1]
				group.Config.Group.Tasks[0], group.Config.Group.Tasks[1] = group.Config.Group.Tasks[1], group.Config.Group.Tasks[0]
				group.Label = "改訂 · task order"
				up := workflow.GroupUpsert{NodeID: group.ID, Label: group.Label, Config: *group.Config.Group, SuccessTarget: "t3", FailureTarget: "oops"}
				if process != nil {
					res := process.call(t, "upsert", "workflow_group_upsert", map[string]any{"name": wf.Name, "group": encode(up)})
					require.False(t, res.IsError, mcpE2EText(t, res))
				} else {
					_, err := ds.workflowSvc.UpsertGroup(ctx, wf.Name, up)
					require.NoError(t, err)
				}
				// Group upsert replaces the original outgoing block in place,
				// preserving the ordinals of unrelated nested-group edges.
				edges := []workflow.Edge{wf.Edges[0]}
				var suffix []workflow.Edge
				for _, edge := range wf.Edges[1:] {
					if edge.Source != group.ID {
						suffix = append(suffix, edge)
					}
				}
				if group.Config.Group.Form == workflow.GroupFormBranches {
					for _, task := range group.Config.Group.Tasks {
						edges = append(edges, workflow.Edge{Source: group.ID, Target: task.Entry, Output: workflow.GroupPortTask, Label: task.Key})
					}
				}
				wf.Edges = append(edges, workflow.Edge{Source: group.ID, Target: "t3", Output: workflow.GroupPortSuccess}, workflow.Edge{Source: group.ID, Target: "oops", Output: workflow.GroupPortFailure})
				wf.Edges = append(wf.Edges, suffix...)
				checkpoint(wf, true)
				if process != nil {
					res := process.call(t, "delete", "workflow_group_delete", map[string]any{"name": wf.Name, "nodeId": group.ID})
					require.False(t, res.IsError, mcpE2EText(t, res))
				} else {
					_, err := ds.workflowSvc.DeleteGroup(ctx, wf.Name, group.ID)
					require.NoError(t, err)
				}
				var deleted workflow.Workflow
				require.NoError(t, json.Unmarshal([]byte(encode(wf)), &deleted))
				deleted.Nodes = append(deleted.Nodes[:1], deleted.Nodes[2:]...)
				deleted.Edges = []workflow.Edge{}
				for _, edge := range wf.Edges {
					if edge.Source != "t2" && edge.Target != "t2" {
						deleted.Edges = append(deleted.Edges, edge)
					}
				}
				checkpoint(&deleted, form == "nested")
				wf.Description = "Full update restores the complete group"
				write(false, wf)
				latest := checkpoint(wf, true)
				// Terminal SQLite state precedes best-effort cache publication.
				// Let the producer publish before shutting down its worker.
				require.Eventually(t, func() bool {
					_, err := os.Stat(filepath.Join(filepath.Dir(env.privatePath(wf.Name)), "layout.json"))
					return err == nil
				}, time.Second, 10*time.Millisecond)
				if process != nil {
					process.finish(t, true)
				}
				ds.layout.stop()
				require.NoError(t, os.Remove(filepath.Join(filepath.Dir(env.privatePath(wf.Name)), "layout.json")))
				require.Equal(t, latest, surfaceView(t, router, "/api/v1/workflows/"+wf.Name+"/layout"))
				require.Equal(t, originalView, surfaceView(t, router, "/api/v1/instances/"+id.String()+"/layout"))
			})
		}
	}
}

const groupBrowserTimeout = 30 * time.Second

// Shared real fixture with a contained loop, a sink and mixed-route crossings.
func layoutTypographyWorkflow() *workflow.Workflow {
	wf := layoutE2EWorkflow("group-typography", "保存したグループ ✅")
	wf.Nodes[1].Type = workflow.NodeTypeParallelGroup
	wf.Nodes[1].Config = workflow.NodeConfig{Group: &workflow.GroupConfig{
		Form:               workflow.GroupFormBranches,
		Tasks:              []workflow.GroupTask{{Key: "a", Entry: "a"}, {Key: "long_configured_task_key", Entry: "b"}},
		CompletionRule:     workflow.GroupRuleFirstSuccess,
		UnfinishedBehavior: workflow.GroupUnfinishedFinishIndependently,
		GatherMode:         workflow.GroupGatherResults,
	}}
	for _, id := range []string{"oops", "a", "b", "a2", "a3", "sink"} {
		n := wf.Nodes[2]
		n.ID = id
		wf.Nodes = append(wf.Nodes, n)
	}
	wf.Edges = []workflow.Edge{
		{Source: "t1", Target: "t2"},
		{Source: "t2", Target: "a", Output: workflow.GroupPortTask, Label: "Same branch label"},
		{Source: "t2", Target: "b", Output: workflow.GroupPortTask, Label: "Same branch label"},
		{Source: "t2", Target: "t3", Output: workflow.GroupPortSuccess},
		{Source: "t2", Target: "oops", Output: workflow.GroupPortFailure},
		{Source: "a", Target: "a2"}, {Source: "a2", Target: "a3"}, {Source: "a3", Target: "a"}, {Source: "a", Target: "sink"},
	}
	return wf
}

type measuredGroupText struct {
	request layout.MeasureRequest
	metrics layout.Measurements
}

// Observe the worker's actual browser output, without replacing or modifying
// any production measurement input or result.
type observedGroupRenderer struct {
	layout.Renderer
	measured chan<- measuredGroupText
}

func (r observedGroupRenderer) Measure(ctx context.Context, req layout.MeasureRequest) (layout.Measurements, error) {
	metrics, err := r.Renderer.Measure(ctx, req)
	if err == nil {
		select {
		case r.measured <- measuredGroupText{request: req, metrics: metrics}:
		case <-ctx.Done():
		}
	}
	return metrics, err
}

// Real measured v2 geometry reaches reviewed SQLite/Reader unchanged.
func TestLayoutGroups_RealBrowserMeasuresCapturedGroupText(t *testing.T) {
	browser := requireBrowserE2E(t)
	measuredText := make(chan measuredGroupText, 1)
	previous := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = previous })
	layoutRenderReviewerFactory = func(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		renderer, reviewer, closeFn, err := newLayoutRenderReviewer(cfg, logger)
		if err != nil {
			return nil, nil, nil, err
		}
		return observedGroupRenderer{Renderer: renderer, measured: measuredText}, reviewer, closeFn, nil
	}
	env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
	useLayoutE2EBrowser(t, browser)
	t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
	ds, _ := env.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	wf := layoutTypographyWorkflow()
	require.NoError(t, ds.workflowSvc.Create(ctx, wf.Name, wf))
	revision := layoutE2ESavedRevision(t, env.privatePath(wf.Name))
	row, err := ds.layout.store.Get(ctx, revision)
	require.NoError(t, err)
	var captured workflow.Workflow
	require.NoError(t, json.Unmarshal(row.Manifest, &captured))
	graph, err := layout.BuildGraph(&captured)
	require.NoError(t, err)
	var observed measuredGroupText
	select {
	case observed = <-measuredText:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-time.After(groupBrowserTimeout):
		t.Fatal("the live worker did not produce browser-measured group text")
	}
	req, measured := observed.request, observed.metrics
	require.Equal(t, layout.MeasureRequestFor(graph, layout.DefaultContract()), req, "the coordinator must feed the saved immutable group's full typography request")
	require.True(t, measured.FontsReady)
	var groupItems int
	for _, item := range req.Items {
		if !strings.HasPrefix(item.Key, `["text",`) {
			continue
		}
		groupItems++
		metric, err := measured.Metric(item.Key)
		require.NoError(t, err, "saved group text %q must be measured by the installed browser", item.Text)
		assert.Positive(t, metric.Width)
		assert.Positive(t, metric.Height)
		assert.Positive(t, metric.Lines)
	}
	require.Positive(t, groupItems)
	short, err := measured.Metric(layout.GroupTextKey(layout.GroupElementID("t2", "lane", "a"), "caption"))
	require.NoError(t, err)
	long, err := measured.Metric(layout.GroupTextKey(layout.GroupElementID("t2", "lane", "long_configured_task_key"), "caption"))
	require.NoError(t, err)
	assert.Greater(t, long.Width, short.Width, "configured task identity reaches actual glyph measurement, not an empty/constant placeholder")

	// Follow the real browser metrics through Arrange and the Worker into the
	// authoritative SQLite candidate and the installed rendered reviewer.
	rec := waitGroupGeometryCheckpoint(t, ds, revision)
	requireGroupReviewCheckpoint(t, rec)
	stored, err := ds.layout.store.Result(ctx, revision, rec.ResultGeneration)
	require.NoError(t, err)
	require.Equal(t, layout.FormatV2, stored.Geometry.Format)
	require.NoError(t, stored.Geometry.Validate(graph))
	require.Len(t, stored.Geometry.GroupElements, 5)
	require.Len(t, stored.Geometry.GroupLanes, 2)
	require.Len(t, stored.Geometry.GroupConnectors, 2)
	for _, e := range stored.Geometry.GroupElements {
		if e.Role == "footer" || e.Role == "lane" {
			require.NotNil(t, e.OverlayBox)
			require.Positive(t, e.OverlayBox.W)
			require.Positive(t, e.OverlayBox.H)
			require.True(t, e.Box.Contains(*e.OverlayBox))
			prefixes := []string{"overlay:status:", "overlay:frozen:", "overlay:badge:winner", "overlay:badge:non-blocking", "overlay:badge:cancel requested"}
			if e.Role == "footer" {
				prefixes = []string{"overlay:status:", "overlay:progress:done:", "overlay:progress:active:", "overlay:progress:not started:", "overlay:progress:failed:", "overlay:winner:"}
			}
			height, width := float64(len(prefixes)-1)*req.Contract.NodeTextGapPx, 0.0
			for _, prefix := range prefixes {
				rowHeight := 0.0
				for _, item := range req.Items {
					var parts []string
					if json.Unmarshal([]byte(item.Key), &parts) != nil || len(parts) != 3 || parts[1] != e.ID || !strings.HasPrefix(parts[2], prefix) {
						continue
					}
					tm, err := measured.Metric(item.Key)
					require.NoError(t, err)
					rowHeight = math.Max(rowHeight, math.Ceil(tm.Height)+1)
					width = math.Max(width, math.Ceil(tm.Width)+1)
				}
				require.Positive(t, rowHeight)
				height += rowHeight
			}
			require.Equal(t, height, e.OverlayBox.H, "persisted combined rows must consume actual browser metrics")
			require.Equal(t, width, e.OverlayBox.W)
		}
	}
	require.NotEmpty(t, stored.Geometry.GroupCrossings, "persist real mixed-route scoring, not a zero-cost placeholder")
	assert.Equal(t, layout.CrossingCount(stored.Geometry), stored.Score.Crossings)
	var header layout.NodeBox
	for _, node := range stored.Geometry.Nodes {
		if node.ID == "t2" {
			header = node
		}
	}
	require.Equal(t, "t2", header.ID)
	c := req.Contract
	var textHeight float64
	for _, key := range []string{layout.NodeLabelKey("t2"), layout.NodeTypeKey("t2"),
		layout.GroupTextKey(layout.GroupElementID("t2", "header", ""), "fanout"),
		layout.GroupTextKey(layout.GroupElementID("t2", "header", ""), "metadata")} {
		tm, err := measured.Metric(key)
		require.NoError(t, err)
		textHeight += math.Ceil(tm.Height) + 1
	}
	wantHeight := math.Ceil(2*c.NodeBorderPx + c.NodeTypeBarPx + 2*c.NodePaddingY + textHeight + 3*c.NodeTextGapPx + layout.DefaultOptions().NodeTextSlack)
	assert.Equal(t, wantHeight, header.Box.H, "saved candidate must reserve the actual browser-measured header supplements")
	view, err := ds.layout.query.WorkflowLayout(ctx, wf.Name)
	require.NoError(t, err)
	require.NotNil(t, view.Geometry)
	assert.Equal(t, layout.GeometryDigest(stored.Geometry), layout.GeometryDigest(*view.Geometry), "Reader must expose that exact reviewed candidate")
}

// Complete group review is authoritative for both authorized revision queries.
func TestLayoutGroups_ReviewerAndStoreTruth(t *testing.T) {
	browser := requireBrowserE2E(t)
	for _, tc := range []struct {
		name, form string
		nested     bool
	}{{"branches", workflow.GroupFormBranches, false}, {"workflows", workflow.GroupFormWorkflows, false}, {"nested", workflow.GroupFormBranches, true}} {
		t.Run(tc.name, func(t *testing.T) {
			env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
			useLayoutE2EBrowser(t, browser)
			t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
			ds, _ := env.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			child := layoutE2EWorkflow("ownership-child", "Child")
			child.Inputs = []workflow.InputField{{Name: "message", Type: "string"}}
			require.NoError(t, ds.workflowSvc.Create(ctx, child.Name, child))
			wf := layoutOwnershipWorkflow(tc.form, tc.nested)
			require.NoError(t, ds.workflowSvc.Create(ctx, wf.Name, wf))
			before, err := os.ReadFile(env.privatePath(wf.Name))
			require.NoError(t, err)
			saved := layoutE2EManifest(t, env.privatePath(wf.Name))
			rec := waitGroupGeometryCheckpoint(t, ds, saved.SavedRevision())
			requireGroupReviewCheckpoint(t, rec)
			require.Equal(t, 1, rec.AttemptsUsed)
			view, err := ds.layout.query.WorkflowLayout(ctx, wf.Name)
			require.NoError(t, err)
			require.Equal(t, layout.DisplayComplete, view.DisplayStatus)
			require.False(t, view.Unreviewed)
			require.False(t, view.RetryEligible)
			stored, err := ds.layout.store.Result(ctx, saved.SavedRevision(), rec.ResultGeneration)
			require.NoError(t, err)
			require.Equal(t, rec.JobGeneration, stored.Generation)
			require.True(t, layout.HasRenderedEvidence(stored.Geometry, stored.Review))
			require.NotNil(t, view.Geometry)
			require.Equal(t, stored.Geometry, *view.Geometry)
			viewed := stored.Review.Evidence.ForViewer()
			require.Equal(t, &viewed, view.Evidence)
			for _, a := range stored.Review.Evidence.Artifacts {
				require.NotEmpty(t, a.Path, "the persisted pass keeps its artifact paths")
			}
			id := surfaceSnapshot(t, ds, saved)
			snapshot, err := ds.layout.query.InstanceLayout(ctx, id)
			require.NoError(t, err)
			require.Equal(t, view, snapshot)
			var rawGeometry, rawReview []byte
			require.NoError(t, ds.db.QueryRow(`SELECT geometry,review FROM layout_results WHERE revision=? AND generation=?`, saved.SavedRevision(), stored.Generation).Scan(&rawGeometry, &rawReview))
			var dbGeometry layout.Geometry
			var dbReview layout.Review
			require.NoError(t, json.Unmarshal(rawGeometry, &dbGeometry))
			require.NoError(t, json.Unmarshal(rawReview, &dbReview))
			require.Equal(t, stored.Geometry, dbGeometry)
			require.Equal(t, *stored.Review, dbReview)
			want, findings := workflow.AnalyzeGroups(saved)
			require.Empty(t, findings)
			assert.Equal(t, want, view.Groups)
			status, body := layoutE2EDo(t, newLayoutE2ERouter(t, ds), http.MethodGet, "/api/v1/workflows/"+wf.Name+"/layout", "")
			require.Equal(t, http.StatusOK, status)
			assert.Equal(t, string(layout.DisplayComplete), body["displayStatus"])
			assert.Equal(t, false, body["unreviewed"])
			assert.Empty(t, body["failureReason"])
			require.NotNil(t, body["evidence"])
			served, err := json.Marshal(body)
			require.NoError(t, err)
			for _, a := range stored.Review.Evidence.Artifacts {
				assert.NotContains(t, string(served), a.Path, "the HTTP view must not carry server artifact paths")
			}
			after, err := os.ReadFile(env.privatePath(wf.Name))
			require.NoError(t, err)
			assert.Equal(t, before, after)
			require.NotNil(t, view.Evidence)
			require.NotNil(t, view.Geometry)
			require.Equal(t, layout.FormatV2, view.Geometry.Format)
			require.NotEmpty(t, view.Geometry.GroupElements)
		})
	}
}

// Group runtime probes require the bounded browser budget rather than the
// ordinary small-input helper's five-second terminal deadline.
func waitGroupGeometryCheckpoint(t *testing.T, ds *domainStack, revision string) layout.Record {
	t.Helper()
	var rec layout.Record
	require.Eventually(t, func() bool {
		var err error
		rec, err = ds.layout.store.Get(context.Background(), revision)
		return err == nil && rec.State.Terminal()
	}, groupBrowserTimeout, 10*time.Millisecond, "group revision %s must reach the durable terminal sink", revision)
	return rec
}

func requireGroupReviewCheckpoint(t *testing.T, rec layout.Record) {
	t.Helper()
	require.Equal(t, layout.StatePassed, rec.State, "%s: %+v", rec.FailureReason, rec.Findings)
	require.Empty(t, rec.FailureStage)
	require.Empty(t, rec.FailureReason)
	require.Zero(t, layout.BlockingCount(rec.Findings))
}

func TestLayoutGroups_InvalidHistoricalInputFailsAtSavedRevisionSink(t *testing.T) {
	env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
	ds, _ := env.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	scope := workflow.Private(localDefaultUser)
	wf := layoutE2EWorkflow("legacy-group", "Legacy")
	wf.Nodes[1].Type = workflow.NodeTypeParallelGroup // A historical, missing group config.
	require.Error(t, ds.workflowSvc.Create(ctx, wf.Name, wf), "normal authoring must still reject invalid groups")
	require.NoFileExists(t, env.privatePath(wf.Name))

	// Reproduce an already saved historical input through the real capture and
	// persistence protocol, below today's authoring validation boundary.
	var req workflow.ArrangementRequest
	require.NoError(t, ds.layout.mutations.WithMutations(ctx, []workflow.MutationTarget{{Scope: scope, Name: wf.Name}}, func(owned context.Context) error {
		var err error
		req, err = ds.layout.stamper.Stamp(owned, scope, wf.Name, wf, nil, workflow.SaveReasonCreate)
		if err != nil {
			return err
		}
		if err = ds.workflowRepo.Create(owned, scope, wf.Name, wf); err != nil {
			return err
		}
		ds.layout.stamper.Committed(owned, req)
		return nil
	}))
	saved, err := os.ReadFile(env.privatePath(wf.Name))
	require.NoError(t, err)
	rec := layoutE2EWaitTerminal(t, ds, req.Revision)
	require.Equal(t, layout.StateFailed, rec.State)
	assert.Equal(t, layout.StageArranging, rec.FailureStage, "invalid groups must fail before browser measurement, not as ordinary boxes")
	assert.Contains(t, rec.FailureReason, layout.ErrInvalidGroups.Error())
	assert.Zero(t, rec.AttemptsUsed)
	assert.Zero(t, rec.ResultGeneration)
	assert.JSONEq(t, string(req.Manifest), string(rec.Manifest))
	view, err := ds.layout.query.WorkflowLayout(ctx, wf.Name)
	require.NoError(t, err)
	assert.Equal(t, req.Revision, view.SavedRevision)
	assert.Equal(t, layout.DisplayFailed, view.DisplayStatus)
	assert.Equal(t, rec.FailureStage, view.FailureStage, "Reader and worker must classify invalid groups at the same stage")
	assert.Contains(t, view.FailureReason, layout.ErrInvalidGroups.Error())
	assert.True(t, view.Unreviewed)
	assert.True(t, view.RetryEligible)
	assert.Nil(t, view.Geometry)
	require.NotNil(t, view.Retained)
	assert.Equal(t, layoutE2EPositions, view.Retained.Positions)
	ack, err := ds.layout.query.RetryWorkflow(ctx, wf.Name, req.Revision)
	require.NoError(t, err)
	assert.Equal(t, rec.JobGeneration+1, ack.JobGeneration)
	rec = layoutE2EWaitTerminal(t, ds, req.Revision)
	assert.Equal(t, ack.JobGeneration, rec.JobGeneration)
	assert.Equal(t, layout.StageArranging, rec.FailureStage)
	after, err := os.ReadFile(env.privatePath(wf.Name))
	require.NoError(t, err)
	assert.Equal(t, saved, after, "inspection and retry never repair authoring bytes")
	var captured workflow.Workflow
	require.NoError(t, json.Unmarshal(rec.Manifest, &captured))
	assert.Nil(t, captured.Nodes[1].Config.Group)
}

// BuildGraph accepts historical type strings, including one whose display
// caption collides with parallel_group. Exercise that exact saved input through
// the shipped page and reviewer: ordinary page capture is not a review pass.
func TestLayoutGroups_HistoricalCaptionCollisionFailsClosed(t *testing.T) {
	browser := requireBrowserE2E(t)
	for _, typ := range []workflow.NodeType{workflow.NodeTypeTransform, "parallel group"} {
		t.Run(string(typ), func(t *testing.T) {
			env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
			useLayoutE2EBrowser(t, browser)
			t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
			ds, _ := env.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			scope := workflow.Private(localDefaultUser)
			wf := layoutE2EWorkflow("historical-caption", "Saved caption ✅")
			wf.Nodes = wf.Nodes[:1]
			wf.Nodes[0].Type = typ
			wf.Edges = nil

			// Historical inputs bypass today's authoring validation, but use the
			// real revision capture, filesystem commit and worker notification.
			var req workflow.ArrangementRequest
			require.NoError(t, ds.layout.mutations.WithMutations(ctx, []workflow.MutationTarget{{Scope: scope, Name: wf.Name}}, func(owned context.Context) error {
				var err error
				req, err = ds.layout.stamper.Stamp(owned, scope, wf.Name, wf, nil, workflow.SaveReasonCreate)
				if err != nil {
					return err
				}
				if err = ds.workflowRepo.Create(owned, scope, wf.Name, wf); err != nil {
					return err
				}
				ds.layout.stamper.Committed(owned, req)
				return nil
			}))
			before, err := os.ReadFile(env.privatePath(wf.Name))
			require.NoError(t, err)
			rec := layoutRenderWaitTerminal(t, ds, req.Revision, 30*time.Second)
			var captured workflow.Workflow
			require.NoError(t, json.Unmarshal(rec.Manifest, &captured))
			require.Equal(t, typ, captured.Nodes[0].Type)
			graph, err := layout.BuildGraph(&captured)
			require.NoError(t, err)
			require.Nil(t, graph.Groups, "the historical caption is not the canonical manifest type")
			require.JSONEq(t, string(req.Manifest), string(rec.Manifest))
			require.Positive(t, rec.ResultGeneration)
			result, err := ds.layout.store.Result(ctx, req.Revision, rec.ResultGeneration)
			require.NoError(t, err)
			require.NoError(t, result.Geometry.Validate(graph))
			require.NotNil(t, result.Review)
			// Real PNGs and reported font readiness establish that the shipped
			// job.ts:parseJob/groupsForJob accepted this Go-produced geometry.
			assert.Equal(t, graph.Nodes[0].TypeCaption, result.Geometry.Nodes[0].TypeCaption)
			require.True(t, result.Review.Evidence.FontsReady)
			if typ == workflow.NodeTypeTransform {
				assertEvidenceOnDisk(t, env, result.Review.Evidence)
			} else {
				// A rejected rendering is never displayable evidence, so its
				// capture is released at once (Must 5).
				assertEvidenceReleased(t, result.Review.Evidence)
			}
			view, err := ds.layout.query.WorkflowLayout(ctx, wf.Name)
			require.NoError(t, err)
			require.NotNil(t, view.Geometry)
			assert.Equal(t, layout.GeometryDigest(result.Geometry), layout.GeometryDigest(*view.Geometry))
			if typ == workflow.NodeTypeTransform {
				require.Equal(t, layout.StatePassed, rec.State, "%+v", rec.Findings)
				assert.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
				assert.Equal(t, layout.DisplayComplete, view.DisplayStatus)
			} else {
				require.Equal(t, layout.StateNeedsAttention, rec.State)
				assert.Equal(t, layout.VerdictFailed, result.Verdict)
				assert.False(t, result.Review.Rendered)
				assert.True(t, view.Unreviewed)
				assert.True(t, view.RetryEligible)
				assert.Nil(t, view.Evidence)
				assert.Equal(t, layout.DisplayNeedsAttention, view.DisplayStatus)
				var identityBlocked bool
				for _, finding := range view.Findings {
					identityBlocked = identityBlocked || finding.Source == layout.SourceReview && finding.Criterion == layout.CriterionRenderMismatch && finding.Severity == layout.SeverityBlocking && finding.Evidence == "missing or mismatched group ownership"
				}
				assert.True(t, identityBlocked, "the actual reviewer must explicitly block the caption/type disagreement")
			}
			after, err := os.ReadFile(env.privatePath(wf.Name))
			require.NoError(t, err)
			assert.Equal(t, before, after, "capture and review must preserve historical saved bytes")
		})
	}
}
