package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pocketbase/dbx"
)

// WorkflowSchedule is the wire/domain representation of a recurring schedule.
// NextRunAt is nil while the schedule is paused.
type WorkflowSchedule struct {
	NamespaceID  string          `json:"namespaceId"`
	ID           string          `json:"id"`
	WorkflowName string          `json:"workflowName"`
	Input        json.RawMessage `json:"input"`
	Cron         string          `json:"cron"`
	Timezone     string          `json:"timezone"`
	NextRunAt    *string         `json:"nextRunAt"`
	CreatedAt    string          `json:"createdAt"`
	UpdatedAt    string          `json:"updatedAt"`
}

type workflowScheduleRow struct {
	NamespaceID  string         `db:"namespace_id"`
	ID           string         `db:"id"`
	WorkflowName string         `db:"workflow_name"`
	Input        sql.NullString `db:"input"`
	Cron         string         `db:"cron"`
	Timezone     string         `db:"timezone"`
	NextRunAt    sql.NullString `db:"next_run_at"`
	CreatedAt    string         `db:"created_at"`
	UpdatedAt    string         `db:"updated_at"`
}

func (r *workflowScheduleRow) toWorkflowSchedule() *WorkflowSchedule {
	return &WorkflowSchedule{
		NamespaceID:  r.NamespaceID,
		ID:           r.ID,
		WorkflowName: r.WorkflowName,
		Input:        nullToRaw(r.Input),
		Cron:         r.Cron,
		Timezone:     r.Timezone,
		NextRunAt:    nullToPtr(r.NextRunAt),
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	}
}

// WorkflowScheduleDefinition is the validated declaration a client registers.
type WorkflowScheduleDefinition struct {
	ID           string
	WorkflowName string
	Input        json.RawMessage
	Cron         string
	Timezone     string
}

// UpsertWorkflowSchedule registers a schedule with last-write-wins semantics.
// Re-registering keeps the dispatch cursor (next_run_at) and the paused state,
// except that an active schedule whose cron or timezone changed restarts at
// its next future occurrence.
func (e *Engine) UpsertWorkflowSchedule(ns string, p WorkflowScheduleDefinition) (*WorkflowSchedule, error) {
	if p.ID == "" {
		return nil, fmt.Errorf("a schedule ID must be a nonempty string: %w", ErrBadRequest)
	}
	if p.WorkflowName == "" {
		return nil, fmt.Errorf("workflowName is required: %w", ErrBadRequest)
	}
	now := time.Now().UTC()
	next, err := nextScheduleOccurrence(p.Cron, p.Timezone, now)
	if err != nil {
		return nil, err
	}

	var row workflowScheduleRow
	err = e.writeDB.NewQuery(`
		INSERT INTO "workflow_schedules" (
			"namespace_id", "id", "workflow_name", "input", "cron", "timezone",
			"next_run_at", "created_at", "updated_at"
		) VALUES ({:ns}, {:id}, {:name}, {:input}, {:cron}, {:tz}, {:next}, {:now}, {:now})
		ON CONFLICT ("namespace_id", "id") DO UPDATE SET
			"workflow_name" = excluded."workflow_name",
			"input" = excluded."input",
			"cron" = excluded."cron",
			"timezone" = excluded."timezone",
			"updated_at" = excluded."updated_at",
			"next_run_at" = CASE
				WHEN "workflow_schedules"."next_run_at" IS NOT NULL
					AND ("workflow_schedules"."cron" <> excluded."cron"
						OR "workflow_schedules"."timezone" <> excluded."timezone")
				THEN excluded."next_run_at"
				ELSE "workflow_schedules"."next_run_at"
			END
		RETURNING *`).Bind(dbx.Params{
		"ns":    ns,
		"id":    p.ID,
		"name":  p.WorkflowName,
		"input": rawToNull(p.Input),
		"cron":  p.Cron,
		"tz":    p.Timezone,
		"next":  isoFromTime(next),
		"now":   isoFromTime(now),
	}).One(&row)
	if err != nil {
		return nil, err
	}
	return row.toWorkflowSchedule(), nil
}

// GetWorkflowSchedule returns a schedule or nil if not found.
func (e *Engine) GetWorkflowSchedule(ns, id string) (*WorkflowSchedule, error) {
	return e.getWorkflowScheduleBy(e.readDB, ns, id)
}

func (e *Engine) getWorkflowScheduleBy(b dbx.Builder, ns, id string) (*WorkflowSchedule, error) {
	var row workflowScheduleRow
	err := b.NewQuery(`SELECT * FROM "workflow_schedules" WHERE "namespace_id" = {:ns} AND "id" = {:id} LIMIT 1`).
		Bind(dbx.Params{"ns": ns, "id": id}).
		One(&row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return row.toWorkflowSchedule(), nil
}

// ListWorkflowSchedules returns a cursor-paginated page of schedules (oldest
// first).
func (e *Engine) ListWorkflowSchedules(ns string, p ListParams) ([]*WorkflowSchedule, paginationMeta, error) {
	limit := clampLimit(p.Limit)
	cur, err := decodeListCursor(p.After, p.Before)
	if err != nil {
		return nil, paginationMeta{}, err
	}
	hasAfter := p.After != ""
	hasBefore := p.Before != ""
	order, op := paginationOrder("ASC", hasBefore)

	where := `"namespace_id" = {:ns}`
	params := dbx.Params{"ns": ns}
	if cur != nil {
		where += ` AND ("created_at", "id") ` + op + ` ({:cca}, {:cid})`
		params["cca"] = cur.CreatedAt
		params["cid"] = cur.ID
	}
	params["lim"] = limit + 1

	query := `SELECT * FROM "workflow_schedules" WHERE ` + where +
		` ORDER BY "created_at" ` + order + `, "id" ` + order + ` LIMIT {:lim}`

	var rows []workflowScheduleRow
	if err := e.readDB.NewQuery(query).Bind(params).All(&rows); err != nil {
		return nil, paginationMeta{}, err
	}

	schedules := make([]*WorkflowSchedule, len(rows))
	for i := range rows {
		schedules[i] = rows[i].toWorkflowSchedule()
	}
	data, meta := buildPaginatedResponse(schedules, limit, hasAfter, hasBefore, func(s *WorkflowSchedule) string {
		return encodeCursor(listCursor{CreatedAt: s.CreatedAt, ID: s.ID})
	})
	return data, meta, nil
}

// PauseWorkflowSchedule pauses (clears next_run_at) or resumes (sets it to the
// next future occurrence) a schedule. Repeating the current state is a no-op
// that returns the schedule unchanged.
func (e *Engine) PauseWorkflowSchedule(ns, id string, paused bool) (*WorkflowSchedule, error) {
	var schedule *WorkflowSchedule
	err := e.tx(func(tx *dbx.Tx) error {
		existing, err := e.getWorkflowScheduleBy(tx, ns, id)
		if err != nil {
			return err
		}
		if err := requireRow(existing != nil, "change schedule pause state"); err != nil {
			return err
		}
		if (existing.NextRunAt == nil) == paused {
			schedule = existing
			return nil
		}

		now := time.Now().UTC()
		var next sql.NullString
		if !paused {
			t, err := nextScheduleOccurrence(existing.Cron, existing.Timezone, now)
			if err != nil {
				return err
			}
			next = sql.NullString{String: isoFromTime(t), Valid: true}
		}
		var row workflowScheduleRow
		if err := tx.NewQuery(`
			UPDATE "workflow_schedules" SET "next_run_at" = {:next}, "updated_at" = {:now}
			WHERE "namespace_id" = {:ns} AND "id" = {:id}
			RETURNING *`).
			Bind(dbx.Params{"next": next, "now": isoFromTime(now), "ns": ns, "id": id}).
			One(&row); err != nil {
			return err
		}
		schedule = row.toWorkflowSchedule()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return schedule, nil
}

// DeleteWorkflowSchedule removes a schedule (idempotent). Runs it already
// created keep their workflow_schedule_id.
func (e *Engine) DeleteWorkflowSchedule(ns, id string) error {
	_, err := e.writeDB.NewQuery(`DELETE FROM "workflow_schedules" WHERE "namespace_id" = {:ns} AND "id" = {:id}`).
		Bind(dbx.Params{"ns": ns, "id": id}).
		Execute()
	return err
}

// EnqueueScheduledRuns creates one pending run for each of up to limit
// schedules due at or before beforeISO, and advances each schedule to its first
// occurrence after `before`, so any number of missed occurrences collapse into
// a single run. It runs in one write transaction on the single-writer pool, so
// concurrent dispatchers never enqueue the same occurrence twice. Returns the
// number of runs created.
func (e *Engine) EnqueueScheduledRuns(ns, beforeISO string, limit int) (int, error) {
	before, err := parseISO(beforeISO)
	if err != nil {
		return 0, fmt.Errorf("invalid before timestamp: %w", ErrBadRequest)
	}
	if limit <= 0 {
		return 0, nil
	}
	beforeISO = isoFromTime(before)
	count := 0
	err = e.tx(func(tx *dbx.Tx) error {
		var due []struct {
			ID           string         `db:"id"`
			WorkflowName string         `db:"workflow_name"`
			Input        sql.NullString `db:"input"`
			Cron         string         `db:"cron"`
			Timezone     string         `db:"timezone"`
		}
		if err := tx.NewQuery(`
			SELECT "id", "workflow_name", "input", "cron", "timezone"
			FROM "workflow_schedules"
			WHERE "namespace_id" = {:ns} AND "next_run_at" <= {:before}
			ORDER BY "next_run_at", "id"
			LIMIT {:lim}`).
			Bind(dbx.Params{"ns": ns, "before": beforeISO, "lim": limit}).
			All(&due); err != nil {
			return err
		}

		for _, s := range due {
			next, err := nextScheduleOccurrence(s.Cron, s.Timezone, before)
			if err != nil {
				return fmt.Errorf("schedule %q: %w", s.ID, err)
			}
			if _, err := tx.NewQuery(`
				UPDATE "workflow_schedules" SET "next_run_at" = {:next}, "updated_at" = {:before}
				WHERE "namespace_id" = {:ns} AND "id" = {:id}`).
				Bind(dbx.Params{"next": isoFromTime(next), "before": beforeISO, "ns": ns, "id": s.ID}).
				Execute(); err != nil {
				return err
			}
			now := isoNow()
			if _, err := tx.NewQuery(`
				INSERT INTO "workflow_runs" (
					"namespace_id", "id", "workflow_name", "status", "config", "input", "attempts",
					"workflow_schedule_id", "available_at", "created_at", "updated_at"
				) VALUES ({:ns}, {:id}, {:name}, 'pending', '{}', {:input}, 0, {:scheduleId}, {:before}, {:now}, {:now})`).
				Bind(dbx.Params{
					"ns":         ns,
					"id":         generateUUID(),
					"name":       s.WorkflowName,
					"input":      s.Input,
					"scheduleId": s.ID,
					"before":     beforeISO,
					"now":        now,
				}).Execute(); err != nil {
				return err
			}
		}
		count = len(due)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
