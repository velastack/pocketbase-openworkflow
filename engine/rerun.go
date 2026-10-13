package engine

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"

	"github.com/pocketbase/dbx"
)

// RerunWorkflowRunParams holds the inputs for RerunWorkflowRun.
type RerunWorkflowRunParams struct {
	// FromStep, when set, keeps the source's successful attempts of every step
	// before it, so the rerun resumes at that step. Nil reruns from scratch.
	FromStep *string
	Context  json.RawMessage
}

// RerunWorkflowRun starts a new run of a finished run's workflow, version,
// input and config. It ports OpenWorkflow's prepareWorkflowRerun (core/rerun.ts)
// and the reference SQLite backend's copy step, in one write transaction.
func (e *Engine) RerunWorkflowRun(ns, sourceID string, p RerunWorkflowRunParams) (*WorkflowRun, error) {
	var run *WorkflowRun
	err := e.tx(func(tx *dbx.Tx) error {
		source, err := e.getWorkflowRunBy(tx, ns, sourceID)
		if err != nil {
			return err
		}
		if source == nil {
			return fmt.Errorf("workflow run %s does not exist: %w", sourceID, ErrNotFound)
		}
		switch source.Status {
		case statusCompleted, statusSucceeded, statusFailed, statusCanceled:
		default:
			return fmt.Errorf("only finished workflow runs can be rerun: %w", ErrConflict)
		}

		var steps []struct {
			StepName  string        `db:"step_name"`
			StepIndex sql.NullInt64 `db:"step_index"`
			Status    string        `db:"status"`
		}
		if p.FromStep != nil {
			if err := tx.NewQuery(`
				SELECT "step_name", "step_index", "status" FROM "step_attempts"
				WHERE "namespace_id" = {:ns} AND "workflow_run_id" = {:runId}
				ORDER BY "created_at", "rowid"`).
				Bind(dbx.Params{"ns": ns, "runId": sourceID}).
				All(&steps); err != nil {
				return err
			}
		}

		var boundary *int64
		if p.FromStep != nil {
			found := false
			for _, s := range steps {
				if s.StepName == *p.FromStep {
					found = true
					if s.StepIndex.Valid {
						v := s.StepIndex.Int64
						boundary = &v
					}
					break
				}
			}
			if !found {
				return fmt.Errorf("step %q does not exist in workflow run %s: %w", *p.FromStep, sourceID, ErrBadRequest)
			}
		}

		context := p.Context
		if p.FromStep != nil {
			// Step order of steps the rerun will not replay from history: those
			// before the boundary that never succeeded. The worker needs it to
			// keep their positions.
			indices := rerunStepIndices(source.Context)
			completed := map[string]bool{}
			for _, s := range steps {
				if !s.StepIndex.Valid {
					return fmt.Errorf("cannot rerun from a step without recorded step order; rerun the entire workflow instead: %w", ErrBadRequest)
				}
				indices[s.StepName] = s.StepIndex.Int64
				if s.Status == "completed" || s.Status == "succeeded" {
					completed[s.StepName] = true
				}
			}
			if boundary != nil {
				for name, index := range indices {
					if index >= *boundary || completed[name] {
						delete(indices, name)
					}
				}
				if len(indices) > 0 {
					obj := map[string]json.RawMessage{}
					if json.Unmarshal(context, &obj) != nil || obj == nil {
						obj = map[string]json.RawMessage{}
					}
					obj["rerunStepIndices"] = mustJSON(indices)
					context = mustJSON(obj)
				}
			}
		}

		run, err = e.insertWorkflowRun(tx, ns, CreateWorkflowRunParams{
			WorkflowName: source.WorkflowName,
			Version:      source.Version,
			Config:       source.Config,
			Context:      context,
			Input:        source.Input,
		})
		if err != nil {
			return err
		}

		if boundary != nil {
			// Same statement as the reference backend, including its in-SQL v4
			// UUIDs for the copied attempts.
			if _, err := tx.NewQuery(`
				INSERT INTO "step_attempts" (
					"namespace_id", "id", "workflow_run_id", "step_name", "step_index", "kind", "status",
					"config", "context", "output", "child_workflow_run_namespace_id", "child_workflow_run_id",
					"started_at", "finished_at", "created_at", "updated_at"
				)
				SELECT "namespace_id",
					lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' ||
						substr(hex(randomblob(2)), 2) || '-' || substr('89ab', (random() & 3) + 1, 1) ||
						substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6))),
					{:newRunId}, "step_name", "step_index", "kind", "status",
					"config", "context", "output", "child_workflow_run_namespace_id", "child_workflow_run_id",
					"started_at", "finished_at", "created_at", "updated_at"
				FROM "step_attempts"
				WHERE "namespace_id" = {:ns} AND "workflow_run_id" = {:sourceId}
					AND "status" IN ('completed', 'succeeded')
					AND "step_index" < {:boundary}`).
				Bind(dbx.Params{"newRunId": run.ID, "ns": ns, "sourceId": sourceID, "boundary": *boundary}).
				Execute(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}

// rerunStepIndices reads context.rerunStepIndices (OpenWorkflow's
// getRerunStepIndices), keeping only non-negative safe integers.
func rerunStepIndices(context json.RawMessage) map[string]int64 {
	out := map[string]int64{}
	var obj map[string]json.RawMessage
	if json.Unmarshal(context, &obj) != nil {
		return out
	}
	var indices map[string]json.RawMessage
	if json.Unmarshal(obj["rerunStepIndices"], &indices) != nil {
		return out
	}
	const maxSafeInteger = 1<<53 - 1
	for name, raw := range indices {
		var f float64
		if string(raw) == "null" || json.Unmarshal(raw, &f) != nil {
			continue
		}
		if f >= 0 && f <= maxSafeInteger && f == math.Trunc(f) {
			out[name] = int64(f)
		}
	}
	return out
}
