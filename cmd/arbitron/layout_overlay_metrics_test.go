package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Installed before the SPA, so observation includes the first displayed rows,
// rather than beginning after a settled-box assertion. Sample after each paint
// opportunity; ResizeObserver's pre-paint containment guard has run by then.
const overlayPaintAudit = `(() => {
 const audit=window.__overlayAudit={frames:0,seen:{},first:{},overflow:[]};
 const visible=e=>{for(let p=e;p;p=p.parentElement){const s=getComputedStyle(p);if(s.display==='none'||s.visibility!=='visible'||+s.opacity===0)return false;}return true;};
 window.__overlayEscapes=e=>{const b=e.getBoundingClientRect();return [...e.querySelectorAll('[data-overlay-row] [data-group-text]')].filter(visible).some(t=>{const range=document.createRange();range.selectNodeContents(t);return [...range.getClientRects()].some(r=>r.left<b.left-.75||r.right>b.right+.75||r.top<b.top-.75||r.bottom>b.bottom+.75);});};
 window.__enlargeOverlay=id=>{const s=document.createElement('style');s.textContent='[data-layout-instance="'+id+'"] [data-group-overlay] span {font-size:100px !important;line-height:1.8 !important}';document.head.append(s);};
 document.addEventListener('DOMContentLoaded',()=>{const id=sessionStorage.getItem('oversizedOverlay');if(id)window.__enlargeOverlay(id);});
 const frame=()=>requestAnimationFrame(()=>setTimeout(()=>{
   audit.frames++;
   for(const e of document.querySelectorAll('[data-group-overlay]')){
     if(!visible(e))continue;
     const texts=[...e.querySelectorAll('[data-overlay-row] [data-group-text]')].filter(visible);
     if(!texts.length)continue;
     const id=e.closest('[data-layout-instance]')?.dataset.layoutInstance||'static';
     audit.seen[id]=(audit.seen[id]||0)+1;
     if(!audit.first[id])audit.first[id]=texts.length;
     if(window.__overlayEscapes(e)||[...e.querySelectorAll('[data-overlay-row]')].some(r=>parseFloat(getComputedStyle(r).height)<=0))audit.overflow.push(id);
   }
   frame();
 },0));frame();
})()`

func TestLayoutGroupSurfaces_ClientOverlayMetricsFailOnlyAffectedTile(t *testing.T) {
	browser := requireBrowserE2E(t)
	env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
	useLayoutE2EBrowser(t, browser)
	ds, _ := env.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	gates := newGroupProcessGates(t)
	child := layoutOwnershipWorkflow(workflow.GroupFormBranches, false)
	child.Name = "ownership-child"
	child.Inputs = []workflow.InputField{{Name: "key", Type: "string", Required: true}}
	for i := range child.Nodes {
		if child.Nodes[i].ID == "a" || child.Nodes[i].ID == "b" {
			child.Nodes[i].Type = workflow.NodeTypeToolCall
			child.Nodes[i].Config = workflow.NodeConfig{Command: "sh", Args: []string{"-c", groupGateScript, "sh", gates.dir, "{{key}}-" + child.Nodes[i].ID}}
		}
	}
	require.NoError(t, ds.workflowSvc.Create(ctx, child.Name, child))
	requireGroupReviewCheckpoint(t, waitGroupGeometryCheckpoint(t, ds, layoutE2ESavedRevision(t, env.privatePath(child.Name))))
	root := layoutOwnershipWorkflow(workflow.GroupFormWorkflows, false)
	for i := range root.Nodes[1].Config.Group.Tasks {
		task := &root.Nodes[1].Config.Group.Tasks[i]
		task.Inputs = map[string]any{"key": task.Key}
	}
	require.NoError(t, ds.workflowSvc.Create(ctx, root.Name, root))
	requireGroupReviewCheckpoint(t, waitGroupGeometryCheckpoint(t, ds, layoutE2ESavedRevision(t, env.privatePath(root.Name))))
	server := surfaceServer(t, ds)
	id, _, err := ds.eng.Trigger(ctx, root.Name, nil, "normal")
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, key := range []string{"first-a", "first-b", "second-a", "second-b"} {
			gates.release(t, key)
		}
		if ds.db.Ping() == nil {
			waitForTerminalState(t, ds, id)
		}
	})
	for _, key := range []string{"first-a", "first-b", "second-a", "second-b"} {
		gates.reached(t, key)
	}
	run := groupRunOf(t, ds, id, "t2")
	first, second := groupRosterTask(t, run, "first").ChildInstanceID, groupRosterTask(t, run, "second").ChildInstanceID
	gates.release(t, "first-a")
	waitForState(t, ds, id, instance.StateCompleted)
	ids := []uuid.UUID{id, first, second}
	baseline := map[uuid.UUID]string{}
	for _, tile := range ids {
		view := surfaceView(t, server.Config.Handler, "/api/v1/instances/"+tile.String()+"/layout")
		require.False(t, view.Unreviewed)
		baseline[tile] = fmt.Sprintf("%s/%d/%d", view.SavedRevision, view.JobGeneration, view.ResultGeneration)
	}
	tab := surfaceBrowser(t)
	_, err = chromedp.Call(tab, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: overlayPaintAudit})
	require.NoError(t, err)
	surfaceReady(t, tab, server.URL+"/instances/"+id.String(), "Layout reviewed")
	clean := fmt.Sprintf(`(() => {const a=window.__overlayAudit;return %s.every(id=>a.seen[id]>2)&&a.frames>5;})()`, fmt.Sprintf(`[%q,%q,%q]`, id.String(), first.String(), second.String()))
	require.NoError(t, chromedp.Do(tab, chromedp.Poll[chromedp.Void](clean, chromedp.WithPollingTimeout(15*time.Second))))
	// A real overflowing glyph Range must be caught by the same observer predicate.
	negative, err := chromedp.Run(tab, chromedp.Evaluate[bool](`(() => {const source=[...document.querySelectorAll('[data-group-overlay]')].find(e=>e.querySelector('[data-group-text]')),e=source.cloneNode(true);e.style.cssText='position:fixed;left:0;top:0;width:20px;height:5px;visibility:visible';for(const s of e.querySelectorAll('[data-group-text]'))s.style.fontSize='100px';document.body.append(e);const escapes=window.__overlayEscapes(e);e.remove();return escapes;})()`))
	require.NoError(t, err)
	require.True(t, negative, "overflowing negative control must escape")
	var overflow []string
	overflow, err = chromedp.Run(tab, chromedp.Evaluate[[]string](`window.__overlayAudit.overflow`))
	require.NoError(t, err)
	require.Empty(t, overflow, "first paint and later clean frames must fit")
	for _, scenario := range []struct {
		name    string
		target  uuid.UUID
		initial bool
	}{
		{"initial-root", id, true}, {"initial-child", first, true}, {"late-child", second, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			target := ""
			if scenario.initial {
				target = scenario.target.String()
			}
			require.NoError(t, chromedp.Do(tab, chromedp.Evaluate[chromedp.Void](fmt.Sprintf(`sessionStorage.setItem('oversizedOverlay',%q)`, target)), chromedp.Navigate(server.URL+"/instances/"+id.String())))
			if !scenario.initial {
				require.NoError(t, chromedp.Do(tab, chromedp.Poll[chromedp.Void](clean, chromedp.WithPollingTimeout(15*time.Second)), chromedp.Evaluate[chromedp.Void](fmt.Sprintf(`requestAnimationFrame(()=>window.__enlargeOverlay(%q))`, scenario.target.String()))))
			}
			sink := fmt.Sprintf(`(() => {const id=%q,all=[...document.querySelectorAll('[data-layout-instance]')],panel=id===%q?[...document.querySelectorAll('[role="status"]')].find(e=>!e.closest('[data-layout-status-instance]')):[...document.querySelectorAll('[data-layout-status-instance]')].find(e=>e.dataset.layoutStatusInstance===id);return panel?.textContent.includes('Client group overlay exceeds reviewed reservation')&&!panel.textContent.includes('Layout reviewed')&&!all.some(e=>e.dataset.layoutInstance===id&&e.hasAttribute('data-arranged-group-element'))&&all.some(e=>e.dataset.layoutInstance===id&&e.dataset.layoutMode==='fallback');})()`, scenario.target.String(), id.String())
			require.NoError(t, chromedp.Do(tab, chromedp.Poll[chromedp.Void](sink, chromedp.WithPollingTimeout(15*time.Second))))
			for _, tile := range ids {
				if tile == scenario.target {
					continue
				}
				ready := fmt.Sprintf(`window.__overlayAudit.seen[%q]>2`, tile.String())
				require.NoError(t, chromedp.Do(tab, chromedp.Poll[chromedp.Void](ready, chromedp.WithPollingTimeout(10*time.Second))))
			}
			overflow, err = chromedp.Run(tab, chromedp.Evaluate[[]string](`window.__overlayAudit.overflow`))
			require.NoError(t, err)
			require.Empty(t, overflow, "no painted frame may overflow")
			for _, tile := range ids {
				view := surfaceView(t, server.Config.Handler, "/api/v1/instances/"+tile.String()+"/layout")
				require.Equal(t, baseline[tile], fmt.Sprintf("%s/%d/%d", view.SavedRevision, view.JobGeneration, view.ResultGeneration))
				require.False(t, view.Unreviewed, "client metrics must not change durable review")
			}
		})
	}
}
