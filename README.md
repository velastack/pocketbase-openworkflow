# pocketbase-openworkflow

An [OpenWorkflow](https://github.com/openworkflowdev/openworkflow)-compatible durable workflow engine for [PocketBase](https://pocketbase.io).

The plugin keeps workflow state in its own SQLite database (`pb_data/openworkflow.db`), serves an HTTP API that mirrors OpenWorkflow's `Backend` interface, and adds a **Workflows** page to the superuser UI. It only uses public PocketBase APIs, so it works with vanilla PocketBase (v0.40.4+) and with [velabase](https://github.com/velastack/velabase).

## Install

```go
import "github.com/velastack/pocketbase-openworkflow"

openworkflow.MustRegister(app, openworkflow.Config{})
```

OpenWorkflow workers connect over HTTP using the [`openworkflow-pocketbase`](https://github.com/velastack/openworkflow-pocketbase) Backend, authenticated as a superuser.

| Config | Default | |
|---|---|---|
| `DBFilename` | `openworkflow.db` | Workflow database file under `pb_data`. |
| `DefaultNamespace` | `default` | Namespace advertised to clients. |
| `AllowedNamespaces` | any | Optional allowlist for the `{namespace}` path segment. |
| `RoutePrefix` | `/api/ow/v1` | Base path of the HTTP API. |
| `DBConnect` | modernc driver | Custom pool opener. Required with the `no_default_driver` build tag. |

The workflow database is quiesced during PocketBase backups, so the copy in the backup archive is consistent.

## Superuser UI

The **Workflows** header link shows run counts by status and a filterable, paginated runs list with auto-refresh. It also has a run detail panel (payloads, steps, child runs and cancel) and a **New Run** form.

The UI is a PocketBase UI extension. Its source is in `web/`, and `ui/` holds the bundled output that gets embedded into the binary. Rebuild it after changing `web/`:

```sh
npm install
npm run build
```

## HTTP API

Every route lives under `{RoutePrefix}/{namespace}` and requires superuser auth.

| Method | Path |
|---|---|
| `POST` / `GET` | `/runs` |
| `GET` | `/runs/counts`, `/runs/{id}` |
| `POST` | `/claim` |
| `POST` | `/runs/{id}/lease`, `/sleep`, `/complete`, `/fail`, `/reschedule`, `/cancel` |
| `POST` / `GET` | `/runs/{id}/steps` |
| `GET` | `/steps/{stepId}`, `/steps/{stepId}/signal` |
| `POST` | `/runs/{id}/steps/{stepId}/complete`, `/fail`, `/child` |
| `POST` | `/signals` |

## Development

```sh
go test ./...
go run ./examples/base serve
```

To work against a local PocketBase or velabase checkout, create a `go.work` (gitignored):

```
go 1.27

use .

replace github.com/pocketbase/pocketbase => ../velabase
```
