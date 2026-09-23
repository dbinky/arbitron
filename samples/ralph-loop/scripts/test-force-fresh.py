import json
import pathlib
import subprocess
import tempfile
import unittest

HERE = pathlib.Path(__file__).parent

class FreshRunTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="ralph-fixture-")
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.git("init", "-q", "-b", "feature")
        self.git("config", "user.name", "Workflow Test")
        self.git("config", "user.email", "workflow-test@example.invalid")
        for file in ["RALPH.md", "docs/reference/focus-areas.md",
                     "docs/reference/paired-reviews.md", "docs/reference/gaps-identified.md"]:
            path = self.repo / file
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("Inherited unrelated review\n")
        self.git("add", ".")
        self.git("commit", "-qm", "fixture")

    def git(self, *args):
        return subprocess.run(["git", "-C", str(self.repo), *args], check=True,
                              capture_output=True, text=True).stdout.strip()

    def launch(self, **extra):
        self.ws = self.root / "instance-12345678"
        self.ws.mkdir(exist_ok=True)
        variables = dict(workspace=str(self.ws), repo_url=self.repo.as_uri(),
                         base_branch="feature", slug="age-filtering")
        variables.update(extra)
        return subprocess.run(["/bin/sh", str(HERE / "init-workspace.sh")],
                              input=json.dumps(variables), capture_output=True, text=True)

    def output(self, **extra):
        result = self.launch(**extra)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def test_default_resumes_existing_plan(self):
        result = self.output()
        self.assertTrue(result["resumed"])
        self.assertEqual(result["branch"], "feature")

    def test_explicit_false_preserves_resume(self):
        self.assertTrue(self.output(force_fresh=False)["resumed"])

    def test_force_fresh_creates_isolated_branch_and_archives(self):
        before = self.git("rev-parse", "HEAD")
        result = self.output(force_fresh=True)
        self.assertFalse(result["resumed"])
        self.assertTrue(result["branch"].startswith("dev-age-filtering-"))
        self.assertEqual(result["base_ref"], "feature")
        archived = subprocess.run(["/bin/sh", str(HERE / "archive-ralph.sh")],
            input=json.dumps(result), capture_output=True, text=True, check=True)
        data = json.loads(archived.stdout)
        self.assertEqual(data["archived_count"], 4)
        for path in data["archived"]:
            self.assertEqual((pathlib.Path(result["work_dir"]) / path).read_text(),
                             "Inherited unrelated review\n")
        self.assertEqual(self.git("rev-parse", "HEAD"), before)

    def test_invalid_flag_rejected_before_clone(self):
        result = self.launch(force_fresh="true")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("force_fresh", result.stderr)
        self.assertFalse((self.ws / "src").exists())

    def test_no_plan_starts_fresh_by_default(self):
        self.git("rm", "docs/reference/focus-areas.md")
        self.git("commit", "-qm", "no plan")
        self.assertFalse(self.output()["resumed"])

    # --- the [skip ci] commit-msg hook -----------------------------------------
    # Every in-loop commit must carry the marker so pushing after every pass does
    # not run CI on intermediate states (one review triggered >1,000 runs in two
    # days). It must be installed on BOTH paths: the resumed run is the case that
    # burned the budget. push-and-pr.sh escapes it with --no-verify.
    def work_git(self, *args):
        return subprocess.run(["git", "-C", str(self.ws / "work"), *args], check=True,
                              capture_output=True, text=True).stdout.strip()

    def assert_hook_marks_loop_commits(self):
        (self.ws / "work" / "note.txt").write_text("loop work\n")
        self.work_git("add", "note.txt")
        self.work_git("commit", "-qm", "refine(age-filtering): single:1/review1 — fix")
        self.assertIn("[skip ci]", self.work_git("log", "-1", "--format=%B"))
        self.work_git("commit", "-q", "--allow-empty", "-m", "approve(age-filtering): single:1/review1 — ticked [ci skip]")
        body = self.work_git("log", "-1", "--format=%B")
        self.assertEqual(body.lower().count("skip"), 1, "an existing marker is not doubled")
        self.work_git("commit", "-q", "--allow-empty", "--no-verify", "-m", "chore(ralph): review loop finished")
        self.assertNotIn("skip ci", self.work_git("log", "-1", "--format=%B"))

    def test_resume_path_installs_the_skip_ci_hook(self):
        self.assertTrue(self.output()["resumed"])
        self.assert_hook_marks_loop_commits()

    def test_fresh_path_installs_the_skip_ci_hook(self):
        self.assertFalse(self.output(force_fresh=True)["resumed"])
        self.assert_hook_marks_loop_commits()

if __name__ == "__main__":
    unittest.main(verbosity=2)
