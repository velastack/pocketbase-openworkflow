package engine

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
)

// Tests for the OpenWorkflow 0.11.0 additions: step indices, recurring
// schedules, reruns, the v6/v7 migrations and canceled signal waiters.

func claimAs(t *testing.T, e *Engine, worker string) *WorkflowRun {
	t.Helper()
	run, err := e.ClaimWorkflowRun(testNS, worker, 60000)
	if err != nil || run == nil {
		t.Fatalf("claim: %v run=%v", err, run)
	}
	return run
}

func int64ptr(v int64) *int64 { return &v }

func TestMigratesV5DatabaseToV7(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openworkflow.db")
	db, err := defaultConnect(path)
	if err != nil {
		t.Fatal(err)
	}
	// Build a v5 database (what a previous release or a native OpenWorkflow
	// 0.10 SQLite backend leaves behind) with a run and a step in it.
	if _, err := db.NewQuery(`CREATE TABLE "openworkflow_migrations" ("version" INTEGER NOT NULL PRIMARY KEY)`).Execute(); err != nil {
		t.Fatal(err)
	}
	for _, m := range owMigrations {
		if m.version > 5 {
			break
		}
		for _, stmt := range m.stmts {
			if _, err := db.NewQuery(stmt).Execute(); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.NewQuery(`INSERT INTO "openworkflow_migrations" ("version") VALUES ({:v})`).Bind(dbx.Params{"v": m.version}).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	now := isoNow()
	for _, stmt := range []string{
		`INSERT INTO "workflow_runs" ("namespace_id","id","workflow_name","status","config","attempts","created_at","updated_at") VALUES ('default','r1','wf','completed','{}',1,'` + now + `','` + now + `')`,
		`INSERT INTO "step_attempts" ("namespace_id","id","workflow_run_id","step_name","kind","status","config","created_at","updated_at") VALUES ('default','s1','r1','a','function','completed','{}','` + now + `','` + now + `')`,
	} {
		if _, err := db.NewQuery(stmt).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	e, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open v5 database: %v", err)
	}
	defer e.Close()
	if v, _ := e.currentMigrationVersion(); v != 7 {
		t.Fatalf("migration version = %d, want 7", v)
	}
	run, err := e.GetWorkflowRun(testNS, "r1")
	if err != nil || run == nil || run.WorkflowScheduleID != nil {
		t.Fatalf("migrated run = %+v, %v", run, err)
	}
	step, err := e.GetStepAttempt(testNS, "s1")
	if err != nil || step == nil || step.StepIndex != nil {
		t.Fatalf("migrated step = %+v, %v", step, err)
	}
	b, _ := json.Marshal(step)
	if !regexp.MustCompile(`"stepIndex":null`).Match(b) {
		t.Fatalf("stepIndex must be emitted as null: %s", b)
	}
}

func TestStepIndexOrdersTiedAttemptsAndPaginates(t *testing.T) {
	e := openTestEngine(t)
	run := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf"})
	claimAs(t, e, "w")
	for _, idx := range []*int64{int64ptr(2), nil, int64ptr(0), int64ptr(1)} {
		if _, err := e.CreateStepAttempt(testNS, CreateStepAttemptParams{
			WorkflowRunID: run.ID, WorkerID: "w", StepName: "s", StepIndex: idx, Kind: kindFunction, Config: raw(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Tie every created_at so only the step index (then id) orders them.
	if _, err := e.writeDB.NewQuery(`UPDATE "step_attempts" SET "created_at" = '2026-01-01T00:00:00.000Z'`).Execute(); err != nil {
		t.Fatal(err)
	}

	var got []string
	var after string
	for {
		page, meta, err := e.ListStepAttempts(testNS, run.ID, ListParams{Limit: 1, After: after})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range page {
			if s.StepIndex == nil {
				got = append(got, "null")
			} else {
				got = append(got, string(rune('0'+*s.StepIndex)))
			}
		}
		if meta.Next == nil {
			break
		}
		after = *meta.Next
	}
	if want := []string{"null", "0", "1", "2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("step order = %v, want %v", got, want)
	}

	// Walking backwards from the end returns the same order.
	all, _, err := e.ListStepAttempts(testNS, run.ID, ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	last := all[len(all)-1]
	before, _, err := e.ListStepAttempts(testNS, run.ID, ListParams{Limit: 10, Before: encodeStepCursor(listCursor{CreatedAt: last.CreatedAt, StepIndex: last.StepIndex, ID: last.ID})})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 3 || before[0].StepIndex != nil || *before[2].StepIndex != 1 {
		t.Fatalf("before page = %+v", before)
	}
}

func TestSendSignalSkipsCanceledWaiters(t *testing.T) {
	e := openTestEngine(t)
	run := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf"})
	claimAs(t, e, "w")
	if _, err := e.CreateStepAttempt(testNS, CreateStepAttemptParams{
		WorkflowRunID: run.ID, WorkerID: "w", StepName: "wait", Kind: kindSignalWait,
		Config: raw(`{}`), Context: raw(`{"kind":"signal-wait","signal":"approve","timeoutAt":null}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CancelWorkflowRun(testNS, run.ID); err != nil {
		t.Fatal(err)
	}
	ids, err := e.SendSignal(testNS, SendSignalParams{Signal: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("signal delivered to canceled run: %v", ids)
	}
}

// --- schedules ---

func TestScheduleLifecycle(t *testing.T) {
	e := openTestEngine(t)
	def := WorkflowScheduleDefinition{ID: "hourly", WorkflowName: "report", Input: raw(`{"a":1}`), Cron: "0 0 * * * *", Timezone: "UTC"}
	first, err := e.UpsertWorkflowSchedule(testNS, def)
	if err != nil {
		t.Fatal(err)
	}
	if first.NextRunAt == nil || string(first.Input) != `{"a":1}` {
		t.Fatalf("upserted = %+v", first)
	}
	next, _ := parseISO(*first.NextRunAt)
	if !next.After(time.Now()) || next.Minute() != 0 || next.Second() != 0 {
		t.Fatalf("nextRunAt = %s", *first.NextRunAt)
	}

	// Re-registering with the same cron keeps the cursor; a new cron resets it.
	def.Input = raw(`{"a":2}`)
	same, err := e.UpsertWorkflowSchedule(testNS, def)
	if err != nil || *same.NextRunAt != *first.NextRunAt || same.CreatedAt != first.CreatedAt || string(same.Input) != `{"a":2}` {
		t.Fatalf("re-register = %+v, %v", same, err)
	}

	// Enqueue collapses every missed occurrence into one run.
	before := isoFromTime(time.Now().UTC().Add(30 * 24 * time.Hour))
	n, err := e.EnqueueScheduledRuns(testNS, before, 10)
	if err != nil || n != 1 {
		t.Fatalf("enqueue = %d, %v", n, err)
	}
	if n, _ := e.EnqueueScheduledRuns(testNS, before, 10); n != 0 {
		t.Fatalf("second enqueue = %d, want 0", n)
	}
	runs, _, err := e.ListWorkflowRuns(testNS, ListRunsParams{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v, %v", runs, err)
	}
	r := runs[0]
	if r.WorkflowScheduleID == nil || *r.WorkflowScheduleID != "hourly" || r.Status != statusPending ||
		r.Version != nil || string(r.Config) != `{}` || string(r.Input) != `{"a":2}` || *r.AvailableAt != before {
		t.Fatalf("scheduled run = %+v", r)
	}
	advanced, _ := e.GetWorkflowSchedule(testNS, "hourly")
	if *advanced.NextRunAt <= before {
		t.Fatalf("nextRunAt %s not after %s", *advanced.NextRunAt, before)
	}

	// Pause clears the cursor; pausing again is a no-op.
	paused, err := e.PauseWorkflowSchedule(testNS, "hourly", true)
	if err != nil || paused.NextRunAt != nil {
		t.Fatalf("pause = %+v, %v", paused, err)
	}
	again, _ := e.PauseWorkflowSchedule(testNS, "hourly", true)
	if !reflect.DeepEqual(again, paused) {
		t.Fatalf("repeated pause changed the schedule: %+v vs %+v", again, paused)
	}
	resumed, err := e.PauseWorkflowSchedule(testNS, "hourly", false)
	if err != nil || resumed.NextRunAt == nil {
		t.Fatalf("resume = %+v, %v", resumed, err)
	}

	if _, err := e.PauseWorkflowSchedule(testNS, "missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pause missing = %v, want ErrNotFound", err)
	}

	// Delete is idempotent and keeps run attribution.
	for i := 0; i < 2; i++ {
		if err := e.DeleteWorkflowSchedule(testNS, "hourly"); err != nil {
			t.Fatal(err)
		}
	}
	if s, _ := e.GetWorkflowSchedule(testNS, "hourly"); s != nil {
		t.Fatalf("deleted schedule still present: %+v", s)
	}
	kept, _ := e.GetWorkflowRun(testNS, r.ID)
	if kept.WorkflowScheduleID == nil || *kept.WorkflowScheduleID != "hourly" {
		t.Fatalf("run lost its schedule id: %+v", kept)
	}
}

func TestUpsertWorkflowScheduleRejectsInvalidDefinitions(t *testing.T) {
	e := openTestEngine(t)
	for _, def := range []WorkflowScheduleDefinition{
		{ID: "", WorkflowName: "wf", Cron: "* * * * *", Timezone: "UTC"},
		{ID: "a", WorkflowName: "wf", Cron: "H * * * *", Timezone: "UTC"},
		{ID: "a", WorkflowName: "wf", Cron: "* * *", Timezone: "UTC"},
		{ID: "a", WorkflowName: "wf", Cron: "* * * * *", Timezone: "Local"},
		{ID: "a", WorkflowName: "wf", Cron: "* * * * *", Timezone: "Mars/Olympus"},
		{ID: "a", WorkflowName: "wf", Cron: "0 0 30 2 *", Timezone: "UTC"},
	} {
		if _, err := e.UpsertWorkflowSchedule(testNS, def); !errors.Is(err, ErrBadRequest) {
			t.Errorf("upsert %+v = %v, want ErrBadRequest", def, err)
		}
	}
}

func TestEnqueueScheduledRunsHonorsLimitAndNamespace(t *testing.T) {
	e := openTestEngine(t)
	for _, id := range []string{"a", "b", "c"} {
		if _, err := e.UpsertWorkflowSchedule(testNS, WorkflowScheduleDefinition{ID: id, WorkflowName: "wf", Cron: "* * * * * *", Timezone: "UTC"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.UpsertWorkflowSchedule("other", WorkflowScheduleDefinition{ID: "a", WorkflowName: "wf", Cron: "* * * * * *", Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	before := isoFromTime(time.Now().UTC().Add(5 * 365 * 24 * time.Hour))
	for _, want := range []int{2, 1, 0} {
		if n, err := e.EnqueueScheduledRuns(testNS, before, 2); err != nil || n != want {
			t.Fatalf("enqueue = %d, %v; want %d", n, err, want)
		}
	}
	counts, _ := e.CountWorkflowRuns("other")
	if counts.Pending != 0 {
		t.Fatalf("other namespace got %d runs", counts.Pending)
	}
	page, meta, err := e.ListWorkflowSchedules(testNS, ListParams{Limit: 2})
	if err != nil || len(page) != 2 || page[0].ID != "a" || meta.Next == nil {
		t.Fatalf("list = %+v %+v %v", page, meta, err)
	}
	rest, _, _ := e.ListWorkflowSchedules(testNS, ListParams{Limit: 2, After: *meta.Next})
	if len(rest) != 1 || rest[0].ID != "c" {
		t.Fatalf("second page = %+v", rest)
	}
}

// --- reruns ---

func TestRerunStepIndicesParsing(t *testing.T) {
	for _, ctx := range []string{``, `null`, `"legacy context"`, `[]`, `{}`, `{"rerunStepIndices":null}`, `{"rerunStepIndices":[0,1]}`, `{"rerunStepIndices":"invalid"}`} {
		if got := rerunStepIndices(raw(ctx)); len(got) != 0 {
			t.Errorf("rerunStepIndices(%s) = %v, want empty", ctx, got)
		}
	}
	got := rerunStepIndices(raw(`{"rerunStepIndices":{"first":0,"later":5,"largest":9007199254740991,"negative":-1,"fractional":1.5,"unsafe":9007199254740992,"numericString":"2","missing":null,"nested":{"index":3}}}`))
	want := map[string]int64{"first": 0, "later": 5, "largest": 9007199254740991}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rerunStepIndices = %v, want %v", got, want)
	}
}

// TestRerunCopiesSuccessfulStepsByIndex ports OpenWorkflow's "copies
// successful steps by index" rerun test.
func TestRerunCopiesSuccessfulStepsByIndex(t *testing.T) {
	e := openTestEngine(t)
	source := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "snapshot"})
	claimAs(t, e, "seed")
	attempt := func(name string, index int64, status string) *StepAttempt {
		step, err := e.CreateStepAttempt(testNS, CreateStepAttemptParams{
			WorkflowRunID: source.ID, WorkerID: "seed", StepName: name, StepIndex: int64ptr(index), Kind: kindFunction, Config: raw(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		switch status {
		case "completed":
			if _, err := e.CompleteStepAttempt(testNS, source.ID, step.ID, "seed", raw(`"`+name+`"`)); err != nil {
				t.Fatal(err)
			}
		case "failed":
			if _, err := e.FailStepAttempt(testNS, source.ID, step.ID, "seed", raw(`{"message":"failed"}`)); err != nil {
				t.Fatal(err)
			}
		}
		return step
	}
	attempt("caught", 0, "failed")
	attempt("unfinished", 1, "running")
	attempt("first", 2, "failed")
	first := attempt("first", 2, "completed")
	second := attempt("second", 3, "completed")
	third := attempt("third", 4, "completed")
	attempt("target", 5, "failed")
	attempt("later", 6, "completed")
	attempt("target", 5, "completed")
	// Earlier writes, tied timestamps, and a legacy status cannot change the prefix.
	for _, q := range []string{
		`UPDATE "step_attempts" SET "created_at" = '2026-01-02T00:00:00.000Z' WHERE "workflow_run_id" = '` + source.ID + `'`,
		`UPDATE "step_attempts" SET "status" = 'succeeded' WHERE "id" = '` + first.ID + `'`,
		`UPDATE "step_attempts" SET "created_at" = '2026-01-01T00:00:00.000Z' WHERE "workflow_run_id" = '` + source.ID + `' AND "step_name" = 'target'`,
	} {
		if _, err := e.writeDB.NewQuery(q).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.CancelWorkflowRun(testNS, source.ID); err != nil {
		t.Fatal(err)
	}

	rerun, err := e.RerunWorkflowRun(testNS, source.ID, RerunWorkflowRunParams{
		FromStep: strptr("target"),
		Context:  raw(`{"traceContext":{"traceparent":"new-trace"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, rerun.Context, `{"traceContext":{"traceparent":"new-trace"},"rerunStepIndices":{"caught":0,"unfinished":1}}`)
	if rerun.Status != statusPending || rerun.IdempotencyKey != nil || rerun.DeadlineAt != nil || rerun.ParentStepAttemptID != nil {
		t.Fatalf("rerun = %+v", rerun)
	}

	saved := map[string]*StepAttempt{}
	for _, s := range []*StepAttempt{first, second, third} {
		got, _ := e.GetStepAttempt(testNS, s.ID)
		saved[got.StepName] = got
	}
	copies, _, err := e.ListStepAttempts(testNS, rerun.ID, ListParams{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for _, c := range copies {
		names = append(names, c.StepName)
		original := saved[c.StepName]
		if original == nil {
			t.Fatalf("unexpected copied step %q", c.StepName)
		}
		if c.ID == original.ID || !uuid.MatchString(c.ID) {
			t.Fatalf("copied step id %q (original %q)", c.ID, original.ID)
		}
		want := *original
		want.ID, want.WorkflowRunID = c.ID, rerun.ID
		if !reflect.DeepEqual(*c, want) {
			t.Fatalf("copied step = %+v, want %+v", *c, want)
		}
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"first", "second", "third"}) {
		t.Fatalf("copied steps = %v", names)
	}

	// A chained rerun carries forward the recorded order of skipped steps.
	if _, err := e.CancelWorkflowRun(testNS, rerun.ID); err != nil {
		t.Fatal(err)
	}
	chained, err := e.RerunWorkflowRun(testNS, rerun.ID, RerunWorkflowRunParams{FromStep: strptr("third")})
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, chained.Context, `{"rerunStepIndices":{"caught":0,"unfinished":1}}`)
	chainedSteps, _, _ := e.ListStepAttempts(testNS, chained.ID, ListParams{})
	names = nil
	for _, s := range chainedSteps {
		names = append(names, s.StepName)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"first", "second"}) {
		t.Fatalf("chained copies = %v", names)
	}
}

func TestRerunRejections(t *testing.T) {
	e := openTestEngine(t)
	run := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf", Version: strptr("v1"), Input: raw(`{"x":1}`)})
	if _, err := e.RerunWorkflowRun(testNS, run.ID, RerunWorkflowRunParams{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("rerun pending = %v, want ErrConflict", err)
	}
	if _, err := e.RerunWorkflowRun(testNS, "missing", RerunWorkflowRunParams{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rerun missing run = %v, want ErrNotFound", err)
	}
	claimAs(t, e, "w")
	for i, name := range []string{"prefix", "target"} {
		step, err := e.CreateStepAttempt(testNS, CreateStepAttemptParams{
			WorkflowRunID: run.ID, WorkerID: "w", StepName: name, StepIndex: int64ptr(int64(i)), Kind: kindFunction, Config: raw(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.CompleteStepAttempt(testNS, run.ID, step.ID, "w", raw(`1`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.CompleteWorkflowRun(testNS, run.ID, "w", raw(`2`)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RerunWorkflowRun(testNS, run.ID, RerunWorkflowRunParams{FromStep: strptr("missing")}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("rerun missing step = %v, want ErrBadRequest", err)
	}

	// Legacy attempts without a step index block partial reruns only.
	if _, err := e.writeDB.NewQuery(`UPDATE "step_attempts" SET "step_index" = NULL WHERE "step_name" = 'prefix'`).Execute(); err != nil {
		t.Fatal(err)
	}
	before, _ := e.CountWorkflowRuns(testNS)
	if _, err := e.RerunWorkflowRun(testNS, run.ID, RerunWorkflowRunParams{FromStep: strptr("target")}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("partial rerun of legacy run = %v, want ErrBadRequest", err)
	}
	if after, _ := e.CountWorkflowRuns(testNS); after != before {
		t.Fatalf("failed rerun created a run: %+v -> %+v", before, after)
	}
	full, err := e.RerunWorkflowRun(testNS, run.ID, RerunWorkflowRunParams{})
	if err != nil {
		t.Fatal(err)
	}
	if full.Context != nil || *full.Version != "v1" || string(full.Input) != `{"x":1}` || full.Attempts != 0 {
		t.Fatalf("full rerun = %+v", full)
	}
	if steps, _, _ := e.ListStepAttempts(testNS, full.ID, ListParams{}); len(steps) != 0 {
		t.Fatalf("full rerun copied %d steps", len(steps))
	}
}

func assertJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("invalid JSON %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}
