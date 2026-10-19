package workflow_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing any lifecycle import gate must allow an invalid manifest to reach
// the real destination tree or mutate its registry/publication metadata.
func TestServiceGroup_ImportsRefuseBeforePersistence(t *testing.T) {
	for _, op := range []string{"copy", "publish", "republish", "duplicate", "rename", "transfer", "backfill"} {
		for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
			for _, defect := range []string{"tasks", "profile", "prompt", "child", "child-store", "checker", "cancelled"} {
				if form == workflow.GroupFormBranches && (defect == "child" || defect == "child-store") {
					continue
				}
				t.Run(op+"/"+form+"/"+defect, func(t *testing.T) {
					root := t.TempDir()
					repo := filesystem.NewWorkflowRepo(root)
					pubs := newFakePublishers()
					reg := newFakeRegistry()
					svc := workflow.NewWorkflowService(repo, pubs)
					svc.SetRegistry(reg)
					svc.SetGroupCheckers(sgProfiles{"agenty": "agent", "chatty": "chat"}, filesystem.NewFileReader(root))
					ctx := ctxWith(sgAlice, true)
					src := workflow.Private(sgAlice)
					if op == "copy" {
						src = workflow.Shared()
					}
					w := sgFilesGroup("source", "agenty", "resolve.md")
					if form == workflow.GroupFormWorkflows {
						w = sgWorkflowsGroup("source", "child", nil, nil)
						w.Nodes[0].Config.Group.ResolverProfile = "agenty"
						w.Nodes[0].Config.Group.ResolverPromptFile = "resolve.md"
						require.NoError(t, repo.Create(ctx, workflow.Shared(), "child", sgChild("child", sgTopic)))
						require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{ID: uuid.New(), Name: "child", Scope: workflow.ScopeShared}))
					}
					want := "GROUP_NO_TASKS"
					switch defect {
					case "tasks":
						w.Nodes[0].Config.Group.Tasks = nil
					case "profile":
						w.Nodes[0].Config.Group.ResolverProfile = "chatty"
						want = "GROUP_RESOLVER_PROFILE_KIND"
					case "prompt":
						want = "GROUP_MISSING_PROMPT_FILE"
					case "child":
						w.Nodes[0].Config.Group.Tasks[0].Workflow = "missing"
						want = "GROUP_UNKNOWN_CHILD_WORKFLOW"
					case "child-store":
						require.NoError(t, os.WriteFile(filepath.Join(root, "shared", "child", "workflow.json"), []byte("{"), 0o600))
						want = "GROUP_CHECKER_UNAVAILABLE"
					case "checker":
						svc.SetGroupCheckers(nil, nil)
						want = "GROUP_CHECKER_UNAVAILABLE"
					}
					require.NoError(t, repo.Create(ctx, src, "source", w))
					if defect != "prompt" {
						require.NoError(t, repo.WriteFile(ctx, src, "source", "resolve.md", []byte("source prompt")))
					}
					if op != "backfill" {
						require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{ID: uuid.New(), Name: "source", Scope: src.Kind, OwnerID: src.OwnerID}))
					}
					if op == "republish" {
						require.NoError(t, repo.Create(ctx, workflow.Shared(), "dest", sgChild("dest")))
						require.NoError(t, pubs.Upsert(ctx, "dest", sgAlice, "source", workflow.VisibilityPublic, ""))
					}
					before := importTree(t, root)
					beforeReg, err := reg.List(ctx)
					require.NoError(t, err)
					beforePub := make(map[string]workflow.Publication)
					for k, v := range pubs.pub {
						beforePub[k] = v
					}
					if defect == "cancelled" {
						var cancel context.CancelFunc
						ctx, cancel = context.WithCancel(ctx)
						cancel()
					}
					switch op {
					case "copy":
						err = svc.Copy(ctx, "source", "dest")
					case "publish", "republish":
						err = svc.Publish(ctx, "source", "dest", workflow.VisibilityPublic, "")
					case "duplicate":
						err = svc.Duplicate(ctx, "source", "dest")
					case "rename":
						err = svc.Rename(ctx, "source", "dest")
					case "transfer":
						err = svc.TransferOwnership(ctx, "source", sgBob)
					case "backfill":
						err = svc.BackfillRegistry(ctx)
					}
					var gve *workflow.GroupValidationError
					if defect == "cancelled" {
						assert.ErrorIs(t, err, context.Canceled)
					} else if assert.ErrorAs(t, err, &gve) {
						assert.Contains(t, sgCodes(gve.Errors), want)
					}
					assert.Equal(t, before, importTree(t, root), "refused import must leave every directory and file byte-identical")
					afterReg, err := reg.List(ctx)
					require.NoError(t, err)
					assert.ElementsMatch(t, beforeReg, afterReg)
					assert.Equal(t, beforePub, pubs.pub)
				})
			}
		}
	}
}

func TestServiceGroup_ImportsAcceptSourcePromptAndAdvisories(t *testing.T) {
	for _, op := range []string{"copy", "publish", "duplicate", "rename", "transfer", "backfill"} {
		t.Run(op, func(t *testing.T) {
			root := t.TempDir()
			repo := filesystem.NewWorkflowRepo(root)
			reg := newFakeRegistry()
			svc := workflow.NewWorkflowService(repo, newFakePublishers())
			svc.SetRegistry(reg)
			svc.SetGroupCheckers(sgProfiles{"agenty": "agent"}, filesystem.NewFileReader(root))
			ctx := ctxWith(sgAlice, true)
			src := workflow.Private(sgBob) // Admin may copy another owner's source.
			if op == "copy" {
				src = workflow.Shared()
			}
			if op == "publish" || op == "backfill" {
				src = workflow.Private(sgAlice)
			}
			w := sgWorkflowsGroup("source", "child", nil, nil) // required-input advisory is permitted
			w.Nodes[0].Config.Group.ResolverProfile = "agenty"
			w.Nodes[0].Config.Group.ResolverPromptFile = "resolve.md"
			require.NoError(t, repo.Create(ctx, workflow.Shared(), "child", sgChild("child", sgTopic)))
			require.NoError(t, repo.Create(ctx, src, "source", w))
			require.NoError(t, repo.WriteFile(ctx, src, "source", "resolve.md", []byte("source prompt")))
			if op != "backfill" {
				require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{ID: uuid.New(), Name: "source", Scope: src.Kind, OwnerID: src.OwnerID}))
			}
			dst, name := workflow.Private(sgAlice), "dest"
			var err error
			switch op {
			case "copy":
				err = svc.Copy(ctx, "source", name)
			case "publish":
				dst = workflow.Shared()
				err = svc.Publish(ctx, "source", name, workflow.VisibilityPublic, "")
			case "duplicate":
				err = svc.Duplicate(ctx, "source", name)
			case "rename":
				dst = src
				err = svc.Rename(ctx, "source", name)
			case "transfer":
				dst = workflow.Private(sgAlice)
				name = "source"
				err = svc.TransferOwnership(ctx, "source", sgAlice)
			case "backfill":
				dst = src
				name = "source"
				err = svc.BackfillRegistry(ctx)
			}
			require.NoError(t, err)
			got, err := repo.Load(ctx, dst, name)
			require.NoError(t, err)
			require.NotNil(t, got.Nodes[0].Config.Group)
			assert.Equal(t, "resolve.md", got.Nodes[0].Config.Group.ResolverPromptFile)
			files, err := repo.ListFiles(ctx, dst, name)
			require.NoError(t, err)
			assert.Contains(t, files, "resolve.md")
		})
	}
}

func importTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[rel] = "directory"
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%x", sha256.Sum256(b))
		return nil
	}))
	return out
}
