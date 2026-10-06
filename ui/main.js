(() => {
  // web/workflowUtils.js
  var TERMINAL_STATUSES = ["completed", "succeeded", "failed", "canceled"];
  function isTerminal(status) {
    return TERMINAL_STATUSES.includes(status);
  }
  function statusClass(status) {
    switch (status) {
      case "completed":
      case "succeeded":
        return "success";
      case "failed":
        return "danger";
      case "running":
      case "sleeping":
        return "info";
      case "pending":
        return "pending";
      case "canceled":
        return "";
      default:
        return "";
    }
  }
  function statusLabel(status) {
    return status ? app.utils.sentenize(status, false) : "-";
  }
  function fmtDate(iso) {
    return iso ? app.utils.toLocalDatetime(iso) : "-";
  }
  function toDisplayDatetime(iso) {
    return iso ? iso.replace("T", " ").replace(/\.\d+/, "") : "";
  }
  function computeDuration(startedAt, finishedAt) {
    if (!startedAt) {
      return "-";
    }
    const start = new Date(startedAt).getTime();
    const end = finishedAt ? new Date(finishedAt).getTime() : Date.now();
    if (isNaN(start) || isNaN(end) || end < start) {
      return "-";
    }
    const totalSeconds = (end - start) / 1e3;
    if (totalSeconds < 1) {
      return `${Math.round(end - start)}ms`;
    }
    if (totalSeconds < 60) {
      return `${totalSeconds.toFixed(1)}s`;
    }
    const totalMinutes = Math.floor(totalSeconds / 60);
    const seconds = Math.round(totalSeconds % 60);
    if (totalMinutes < 60) {
      return `${totalMinutes}m ${seconds}s`;
    }
    const hours = Math.floor(totalMinutes / 60);
    const minutes = totalMinutes % 60;
    return `${hours}h ${minutes}m`;
  }
  function stepAttemptIndexById(steps) {
    const groups = {};
    const sorted = (steps || []).slice().sort((a, b) => (a.createdAt || "").localeCompare(b.createdAt || ""));
    for (const s of sorted) {
      (groups[s.stepName] = groups[s.stepName] || []).push(s);
    }
    const map = {};
    for (const name in groups) {
      const arr = groups[name];
      arr.forEach((s, i) => {
        map[s.id] = { index: i + 1, total: arr.length };
      });
    }
    return map;
  }
  function startPolling(fn, interval) {
    let timer = null;
    function start() {
      timer = timer || setInterval(fn, interval);
    }
    function stop() {
      clearInterval(timer);
      timer = null;
    }
    function onVisibilityChange() {
      if (document.hidden) {
        stop();
      } else {
        fn();
        start();
      }
    }
    if (!document.hidden) {
      start();
    }
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      stop();
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }

  // web/workflowRunCreatePanel.js
  window.app = window.app || {};
  window.app.modals = window.app.modals || {};
  window.app.modals.openWorkflowRunCreate = function(opts = {}) {
    app.store.errors = null;
    const modal = workflowRunCreateModal(opts);
    document.body.appendChild(modal);
    app.modals.open(modal);
  };
  function workflowRunCreateModal(opts) {
    const namespace = opts.namespace || "default";
    const uniqueId = "workflow_run_create_" + app.utils.randomString();
    const formId = uniqueId + "_form";
    let modal;
    const data = store({
      workflowName: "",
      version: "",
      inputText: "",
      availableAt: "",
      deadlineAt: "",
      inputError: "",
      isSaving: false,
      get canSave() {
        return !data.isSaving && data.workflowName.trim() !== "";
      }
    });
    function resetForm() {
      data.workflowName = "";
      data.version = "";
      data.inputText = "";
      data.availableAt = "";
      data.deadlineAt = "";
      data.inputError = "";
    }
    async function save(close = true) {
      if (!data.canSave) {
        return;
      }
      const body = { workflowName: data.workflowName.trim() };
      const version = data.version.trim();
      if (version) {
        body.version = version;
      }
      const inputTrimmed = data.inputText.trim();
      if (inputTrimmed) {
        let parsed;
        try {
          parsed = JSON.parse(inputTrimmed);
        } catch (_) {
          data.inputError = "Invalid JSON.";
          return;
        }
        data.inputError = "";
        body.input = parsed;
      }
      if (data.availableAt) {
        body.availableAt = app.utils.toRFC3339Datetime(data.availableAt);
      }
      if (data.deadlineAt) {
        body.deadlineAt = app.utils.toRFC3339Datetime(data.deadlineAt);
      }
      data.isSaving = true;
      try {
        const res = await app.pb.send(`/api/ow/v1/${encodeURIComponent(namespace)}/runs`, {
          method: "POST",
          body
        });
        app.toasts.success("Successfully created workflow run.", { key: "workflowRunCreate" });
        opts.onsave?.(res.run);
        data.isSaving = false;
        if (close) {
          app.modals.close(modal, true);
        } else {
          resetForm();
        }
      } catch (err) {
        if (!err?.isAbort) {
          data.isSaving = false;
          app.checkApiError(err);
        }
      }
    }
    function textField(label, key, attrs = {}) {
      const id = uniqueId + "_" + key;
      return t.div(
        { className: "field" },
        t.label({ htmlFor: id }, t.span({ className: "txt" }, label)),
        t.input({
          id,
          type: "text",
          value: () => data[key],
          oninput: (e) => data[key] = e.target.value,
          ...attrs
        })
      );
    }
    function datetimeField(label, key, help) {
      const id = uniqueId + "_" + key;
      return t.div(
        { className: "ow-field" },
        t.div(
          { className: "field" },
          t.label({ htmlFor: id }, t.span({ className: "txt" }, label)),
          t.input({
            id,
            step: 1,
            type: "datetime-local",
            value: () => data[key],
            onchange: (e) => data[key] = e.target.value
          })
        ),
        help ? t.div({ className: "field-help" }, help) : t.div({})
      );
    }
    modal = t.div(
      {
        pbEvent: "workflowRunCreatePanel",
        className: "modal workflow-run-create-panel",
        onbeforeopen: (el) => {
          resetForm();
          data.isSaving = false;
          return opts.onbeforeopen?.(el);
        },
        onafteropen: (el) => opts.onafteropen?.(el),
        onbeforeclose: (el) => opts.onbeforeclose?.(el),
        onafterclose: (el) => {
          opts.onafterclose?.(el);
          el?.remove();
        }
      },
      t.header(
        { className: "modal-header" },
        t.h5({ className: "modal-title" }, "New workflow run")
      ),
      t.form(
        {
          id: formId,
          className: "modal-content",
          onsubmit: (e) => {
            e.preventDefault();
            save(true);
          }
        },
        t.div(
          {
            className: "grid",
            inert: () => data.isSaving,
            onmount: (el) => {
              el._quickSaveHandler = (e) => {
                if ((e.ctrlKey || e.metaKey) && e.code == "KeyS") {
                  e.preventDefault();
                  save(false);
                }
              };
              window.addEventListener("keydown", el._quickSaveHandler);
            },
            onunmount: (el) => {
              if (el?._quickSaveHandler) {
                window.removeEventListener("keydown", el._quickSaveHandler);
              }
            }
          },
          t.div(
            { className: "col-12" },
            textField("Workflow Name", "workflowName", {
              required: true,
              placeholder: "e.g. hello-world",
              autofocus: true
            })
          ),
          t.div(
            { className: "col-12" },
            t.div(
              { className: "field" },
              t.label(
                { htmlFor: uniqueId + "_input" },
                t.span({ className: "txt" }, "Input (JSON)"),
                t.span(
                  {
                    hidden: () => isValidStringifiedJSON(data.inputText.trim()),
                    className: "json-state",
                    ariaDescription: app.attrs.tooltip("Invalid JSON", "left")
                  },
                  t.i({ className: "ri-error-warning-fill txt-danger", ariaHidden: true })
                ),
                t.span(
                  {
                    hidden: () => !isValidStringifiedJSON(data.inputText.trim()),
                    className: "json-state",
                    ariaDescription: app.attrs.tooltip("Valid JSON", "left")
                  },
                  t.i({ className: "ri-checkbox-circle-fill txt-success", ariaHidden: true })
                )
              ),
              app.components.codeEditor({
                language: "js",
                id: uniqueId + "_input",
                value: () => data.inputText,
                oninput: (val) => {
                  data.inputText = val;
                  data.inputError = "";
                }
              }),
              () => data.inputError ? t.div({ className: "help-block help-block-error" }, data.inputError) : t.div({})
            )
          ),
          t.div(
            { className: "col-12 col-sm-6" },
            datetimeField("Schedule For", "availableAt", "Leave empty to run immediately.")
          ),
          t.div(
            { className: "col-12 col-sm-6" },
            datetimeField("Deadline", "deadlineAt", "Leave empty for no deadline.")
          ),
          t.div(
            { className: "col-12" },
            textField("Version", "version", { placeholder: "optional" })
          )
        )
      ),
      t.footer(
        { className: "modal-footer" },
        t.button(
          {
            type: "button",
            className: "btn transparent m-r-auto",
            disabled: () => data.isSaving,
            onclick: () => app.modals.close(modal)
          },
          t.span({ className: "txt" }, "Close")
        ),
        t.div(
          { className: "btns" },
          t.button(
            {
              type: "submit",
              "html-form": formId,
              className: () => `btn expanded-lg ${data.isSaving ? "loading" : ""}`,
              disabled: () => !data.canSave
            },
            t.span({ className: "txt" }, "Create")
          ),
          t.button(
            {
              type: "button",
              className: "btn p-5",
              title: "Save options",
              disabled: () => !data.canSave,
              "html-popovertarget": uniqueId + "save_options"
            },
            t.i({ className: "ri-arrow-up-s-line", ariaHidden: true })
          ),
          t.div(
            { id: uniqueId + "save_options", className: "dropdown nowrap", popover: "auto" },
            t.button(
              {
                type: "button",
                className: "dropdown-item",
                onclick: (e) => {
                  e.target.closest(".dropdown").hidePopover();
                  save(false);
                }
              },
              t.span({ className: "txt" }, "Save and continue"),
              t.small({ className: "txt-hint" }, "(Ctrl+S)")
            ),
            t.hr(),
            t.button(
              {
                type: "button",
                className: "dropdown-item",
                onclick: (e) => {
                  e.target.closest(".dropdown").hidePopover();
                  resetForm();
                }
              },
              t.span({ className: "txt" }, "Reset form")
            )
          )
        )
      )
    );
    return modal;
  }
  function isValidStringifiedJSON(val) {
    if (val === "") {
      return true;
    }
    try {
      JSON.parse(val);
      return true;
    } catch (_) {
      return false;
    }
  }

  // web/workflowRunDetailPanel.js
  var POLL_INTERVAL = 5e3;
  window.app = window.app || {};
  window.app.modals = window.app.modals || {};
  window.app.modals.openWorkflowRunDetail = function(opts = {}) {
    if (!opts.runId) {
      console.warn("[openWorkflowRunDetail] missing required runId");
      return;
    }
    const modal = workflowRunDetailModal(opts);
    document.body.appendChild(modal);
    app.modals.open(modal);
  };
  function workflowRunDetailModal(opts) {
    const namespace = opts.namespace || "default";
    const runId = opts.runId;
    const base = `/api/ow/v1/${encodeURIComponent(namespace)}`;
    let modal;
    const data = store({
      loading: true,
      run: null,
      steps: [],
      parentRunId: null
    });
    async function load(silent = false) {
      if (!silent) {
        data.loading = true;
      }
      try {
        const [runRes, stepsRes] = await Promise.all([
          app.pb.send(`${base}/runs/${runId}`, { method: "GET", requestKey: "ow_run_detail" }),
          app.pb.send(`${base}/runs/${runId}/steps`, {
            method: "GET",
            query: { limit: 200 },
            requestKey: "ow_run_detail_steps"
          })
        ]);
        const steps = stepsRes.data || [];
        if (!silent || JSON.stringify(runRes.run) !== JSON.stringify(data.run)) {
          data.run = runRes.run;
        }
        if (!silent || JSON.stringify(steps) !== JSON.stringify(data.steps)) {
          data.steps = steps;
        }
        if (data.run?.parentStepAttemptId) {
          try {
            const pres = await app.pb.send(`${base}/steps/${data.run.parentStepAttemptId}`, {
              method: "GET",
              requestKey: "ow_run_detail_parent"
            });
            data.parentRunId = pres.stepAttempt?.workflowRunId || null;
          } catch (_) {
          }
        }
      } catch (err) {
        if (!err.isAbort && !silent) app.checkApiError(err);
      }
      if (!silent) {
        data.loading = false;
      }
    }
    let stopPolling = null;
    function poll() {
      if (data.run && !isTerminal(data.run.status)) {
        load(true);
      }
    }
    function cancel() {
      if (!data.run) {
        return;
      }
      app.modals.confirm(`Do you really want to cancel workflow run "${data.run.workflowName}"?`, async () => {
        try {
          const res = await app.pb.send(`${base}/runs/${runId}/cancel`, { method: "POST", body: {} });
          data.run = res.run;
          opts.oncancel?.(res.run);
        } catch (err) {
          app.checkApiError(err);
        }
      });
    }
    function navigate(id) {
      if (id) {
        opts.onnavigate?.(id);
      }
    }
    function metaItem(label, value, mono) {
      return t.div(
        { className: "ow-meta-item" },
        t.span({ className: "ow-meta-label" }, label),
        t.span({ className: mono ? "ow-meta-value txt-mono" : "ow-meta-value" }, value ?? "-")
      );
    }
    function runLink(label, id) {
      return t.button(
        {
          type: "button",
          className: "btn sm transparent ow-run-link",
          onclick: (e) => {
            e.preventDefault();
            e.stopPropagation();
            navigate(id);
          }
        },
        t.span({ className: "txt-mono" }, label)
      );
    }
    function jsonFieldCol(label, value) {
      if (value === null || value === void 0) {
        return null;
      }
      const text = typeof value === "string" ? value : JSON.stringify(value, null, 2);
      return t.div(
        { className: "col-12" },
        t.div(
          { className: "field" },
          t.label({}, t.span({ className: "txt" }, label)),
          app.components.codeEditor({
            language: "js",
            disabled: true,
            value: () => text
          })
        )
      );
    }
    function metaGrid() {
      const run = data.run;
      return t.div(
        { className: "ow-detail-meta" },
        metaItem("ID", run.id, true),
        metaItem("Attempts", "" + run.attempts),
        metaItem("Duration", computeDuration(run.startedAt, run.finishedAt), true),
        metaItem("Version", run.version, true),
        metaItem("Worker", run.workerId, true),
        metaItem("Idempotency Key", run.idempotencyKey, true),
        metaItem("Created", fmtDate(run.createdAt)),
        metaItem("Started", fmtDate(run.startedAt)),
        metaItem("Finished", fmtDate(run.finishedAt)),
        metaItem("Available", fmtDate(run.availableAt)),
        metaItem("Deadline", fmtDate(run.deadlineAt)),
        () => data.parentRunId ? t.div(
          { className: "ow-meta-item" },
          t.span({ className: "ow-meta-label" }, "Parent Run"),
          runLink(data.parentRunId, data.parentRunId)
        ) : t.div({})
      );
    }
    function runPayloads() {
      const run = data.run;
      const fields = [
        jsonFieldCol("Input", run.input),
        jsonFieldCol("Output", run.output),
        jsonFieldCol("Error", run.error),
        jsonFieldCol("Context", run.context)
      ].filter(Boolean);
      return fields.length ? t.div({ className: "grid ow-payloads" }, ...fields) : t.div({});
    }
    function stepAccordion(s, idx) {
      const bodyChildren = [
        t.div(
          { className: "col-12 ow-step-meta" },
          t.span({}, "Started: " + fmtDate(s.startedAt)),
          t.span({}, "Duration: " + computeDuration(s.startedAt, s.finishedAt)),
          s.childWorkflowRunId ? runLink("child run \u2192", s.childWorkflowRunId) : t.span({})
        ),
        jsonFieldCol("Output", s.output),
        jsonFieldCol("Error", s.error),
        jsonFieldCol("Context", s.context)
      ].filter(Boolean);
      return t.details(
        { className: "accordion ow-step-accordion" },
        t.summary(
          null,
          t.span({ className: "txt txt-mono" }, s.stepName),
          t.small(
            { className: "txt-hint ow-step-sub" },
            s.kind + (idx ? ` \xB7 ${idx.index}/${idx.total}` : "")
          ),
          t.span({ className: "label m-l-auto " + statusClass(s.status) }, statusLabel(s.status))
        ),
        t.div({ className: "grid" }, ...bodyChildren)
      );
    }
    function stepsList() {
      if (data.loading) {
        return t.div({ className: "txt-hint ow-steps-empty" }, "Loading...");
      }
      if (!data.steps.length) {
        return t.div({ className: "txt-hint ow-steps-empty" }, "No steps.");
      }
      const idxMap = stepAttemptIndexById(data.steps);
      return t.div({ className: "ow-steps" }, data.steps.map((s) => stepAccordion(s, idxMap[s.id])));
    }
    modal = t.div(
      {
        pbEvent: "workflowRunDetailPanel",
        className: "modal workflow-run-detail-panel",
        onbeforeopen: (el) => {
          load();
          stopPolling = startPolling(poll, POLL_INTERVAL);
          return opts.onbeforeopen?.(el);
        },
        onafteropen: (el) => opts.onafteropen?.(el),
        onbeforeclose: (el) => opts.onbeforeclose?.(el),
        onafterclose: (el) => {
          stopPolling?.();
          opts.onafterclose?.(el);
          el?.remove();
        }
      },
      t.header(
        { className: "modal-header ow-detail-header" },
        () => data.run ? t.h5({ className: "modal-title txt-mono" }, data.run.workflowName) : t.h5({ className: "modal-title" }, "Workflow run"),
        () => data.run ? t.span(
          { className: "label m-l-10 " + statusClass(data.run.status) },
          statusLabel(data.run.status)
        ) : t.span({})
      ),
      t.div(
        { className: "modal-content ow-detail" },
        () => {
          if (data.loading) {
            return t.div({ className: "ow-detail-loading txt-hint" }, "Loading...");
          }
          if (!data.run) {
            return t.div({ className: "alert alert-danger" }, "Failed to load workflow run.");
          }
          return t.div(
            { className: "ow-detail-body" },
            metaGrid(),
            runPayloads(),
            t.h6({ className: "ow-steps-title" }, "Steps"),
            stepsList()
          );
        }
      ),
      t.footer(
        { className: "modal-footer" },
        t.button(
          { type: "button", className: "btn transparent m-r-auto", onclick: () => app.modals.close(modal) },
          t.span({ className: "txt" }, "Close")
        ),
        () => data.run && !isTerminal(data.run.status) ? t.button(
          { type: "button", className: "btn danger", onclick: cancel },
          t.span({ className: "txt" }, "Cancel run")
        ) : t.span({})
      )
    );
    return modal;
  }

  // web/pageWorkflows.js
  var NAMESPACE = "default";
  var PAGE_SIZES = [25, 50, 100];
  var POLL_INTERVAL2 = 5e3;
  var RUN_QUERY_KEY = "run";
  var CARD_ORDER = ["pending", "running", "completed", "failed", "canceled"];
  function pageWorkflows(route) {
    app.store.title = "Workflows";
    const data = store({
      runs: [],
      // cursors of the adjacent pages, as returned with the current page
      prev: null,
      next: null,
      // the current page position -- at most one of after/before is set
      // (both empty means the first page); persisted in the url together
      // with the page size and the filters so a page is deep-linkable
      after: route.query.after?.[0] || "",
      before: route.query.after?.[0] ? "" : route.query.before?.[0] || "",
      pageSize: resolvePageSize(route.query.limit?.[0]),
      loading: false,
      counts: null,
      statusFilter: CARD_ORDER.includes(route.query.status?.[0]) ? route.query.status[0] : "",
      nameFilter: route.query.name?.[0] || "",
      activeRunId: route.query[RUN_QUERY_KEY]?.[0] || "",
      get hasFilters() {
        return !!data.statusFilter || !!data.nameFilter.trim();
      }
    });
    const uid = app.utils.randomString(6);
    const runsKey = "ow_runs_" + uid;
    const countsKey = "ow_counts_" + uid;
    let nameDebounce = null;
    function apiBase() {
      return `/api/ow/v1/${encodeURIComponent(NAMESPACE)}`;
    }
    function replaceRun(run) {
      const i = data.runs.findIndex((r) => r.id === run.id);
      if (i >= 0) {
        data.runs[i] = run;
        data.runs = data.runs.slice();
      }
    }
    async function loadCounts() {
      try {
        const res = await app.pb.send(`${apiBase()}/runs/counts`, { method: "GET", requestKey: countsKey });
        data.counts = res.counts;
      } catch (err) {
        if (!err.isAbort) app.checkApiError(err);
      }
    }
    async function loadRuns(silent = false) {
      if (!silent) {
        data.loading = true;
      }
      try {
        const query = { limit: data.pageSize };
        if (data.statusFilter) {
          query.status = data.statusFilter;
        }
        const name = data.nameFilter.trim();
        if (name) {
          query.workflowNameContains = name;
        }
        if (data.after) {
          query.after = data.after;
        } else if (data.before) {
          query.before = data.before;
        }
        const res = await app.pb.send(`${apiBase()}/runs`, { method: "GET", query, requestKey: runsKey });
        const runs = res.data || [];
        if (!silent || JSON.stringify(runs) !== JSON.stringify(data.runs)) {
          data.runs = runs;
        }
        data.prev = res.pagination?.prev || null;
        data.next = res.pagination?.next || null;
      } catch (err) {
        if (!err.isAbort && !silent) app.checkApiError(err);
      }
      if (!silent) {
        data.loading = false;
      }
    }
    function syncQueryParams() {
      app.utils.replaceHashQueryParams({
        limit: data.pageSize === PAGE_SIZES[0] ? null : data.pageSize,
        after: data.after,
        before: data.before,
        status: data.statusFilter,
        name: data.nameFilter.trim()
      });
    }
    function goToPage(after, before) {
      data.after = after || "";
      data.before = before || "";
      syncQueryParams();
      loadRuns();
    }
    function toggleStatusFilter(status) {
      data.statusFilter = data.statusFilter === status ? "" : status;
      goToPage();
    }
    function setNameFilter(value) {
      data.nameFilter = value;
      clearTimeout(nameDebounce);
      nameDebounce = setTimeout(() => goToPage(), 300);
    }
    function clearFilters() {
      clearTimeout(nameDebounce);
      data.statusFilter = "";
      data.nameFilter = "";
      goToPage();
    }
    function setPageSize(size) {
      data.pageSize = resolvePageSize(size);
      goToPage();
    }
    function cancelRun(run) {
      app.modals.confirm(`Do you really want to cancel workflow run "${run.workflowName}"?`, async () => {
        try {
          const res = await app.pb.send(`${apiBase()}/runs/${run.id}/cancel`, { method: "POST", body: {} });
          replaceRun(res.run);
          loadCounts();
        } catch (err) {
          app.checkApiError(err);
        }
      });
    }
    function openNewRun() {
      app.modals.openWorkflowRunCreate({
        namespace: NAMESPACE,
        onsave: () => {
          goToPage();
          loadCounts();
        }
      });
    }
    const watchers = [
      watch(
        () => data.activeRunId,
        (newVal, oldVal) => {
          if (newVal === oldVal) {
            return;
          }
          if (!data.activeRunId) {
            app.utils.replaceHashQueryParams({ [RUN_QUERY_KEY]: null });
            return;
          }
          const openedRunId = data.activeRunId;
          app.utils.replaceHashQueryParams({ [RUN_QUERY_KEY]: openedRunId });
          app.modals.close(null, true);
          app.modals.openWorkflowRunDetail({
            namespace: NAMESPACE,
            runId: openedRunId,
            onnavigate: (id) => {
              data.activeRunId = id;
            },
            oncancel: (run) => {
              replaceRun(run);
              loadCounts();
            },
            onafterclose: () => {
              if (data.activeRunId === openedRunId) {
                data.activeRunId = "";
              }
            }
          });
        }
      )
    ];
    loadRuns();
    loadCounts();
    const stopPolling = startPolling(() => {
      loadRuns(true);
      loadCounts();
    }, POLL_INTERVAL2);
    return t.div(
      {
        pbEvent: "pageWorkflows",
        className: "page page-workflows",
        onunmount: () => {
          clearTimeout(nameDebounce);
          stopPolling();
          app.pb.cancelRequest(countsKey);
          app.pb.cancelRequest(runsKey);
          watchers.forEach((w) => w?.unwatch());
        }
      },
      t.div(
        { className: "page-content full-height" },
        t.header(
          { className: "page-header" },
          t.nav({ className: "breadcrumbs" }, t.div({ className: "breadcrumb-item" }, "Workflows")),
          t.div({ className: "flex-fill" }),
          t.div(
            { className: "ow-name-filter" },
            t.i({ className: "ri-search-line" }),
            t.input({
              type: "text",
              placeholder: "Filter by workflow name",
              value: () => data.nameFilter,
              oninput: (e) => setNameFilter(e.target.value)
            })
          ),
          () => data.hasFilters ? t.button(
            {
              type: "button",
              className: "btn secondary",
              onclick: clearFilters
            },
            t.i({ className: "ri-close-line" }),
            t.span({ className: "txt" }, "Clear filters")
          ) : t.div({}),
          t.button(
            {
              type: "button",
              className: "btn secondary",
              onclick: () => {
                loadRuns();
                loadCounts();
              }
            },
            t.i({ className: "ri-refresh-line" }),
            t.span({ className: "txt" }, "Refresh")
          ),
          t.button(
            { type: "button", className: "btn", onclick: openNewRun },
            t.i({ className: "ri-add-line" }),
            t.span({ className: "txt" }, "New Run")
          )
        ),
        () => data.counts ? t.div(
          { className: "ow-cards" },
          ...CARD_ORDER.map(
            (k) => t.div(
              {
                className: () => "ow-card ow-card-" + k + (data.statusFilter === k ? " ow-card-active" : ""),
                "html-role": "button",
                tabIndex: 0,
                title: `Show only ${statusLabel(k).toLowerCase()} runs`,
                "html-aria-pressed": () => data.statusFilter === k ? "true" : "false",
                onclick: () => toggleStatusFilter(k),
                onkeydown: (e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault();
                    toggleStatusFilter(k);
                  }
                }
              },
              t.span({ className: "ow-card-value txt-mono" }, "" + (data.counts[k] ?? 0)),
              t.span({ className: "ow-card-label" }, statusLabel(k))
            )
          )
        ) : t.div({}),
        t.table(
          { className: "table" },
          t.thead(
            {},
            t.tr(
              {},
              t.th({}, "Workflow"),
              t.th({}, "Status"),
              t.th({}, "Attempts"),
              t.th({}, "Created"),
              t.th({}, "Duration"),
              t.th({}, "")
            )
          ),
          t.tbody({}, () => data.runs.length ? data.runs.map(
            (run) => t.tr(
              { className: "ow-run-row", onclick: () => data.activeRunId = run.id },
              t.td(
                {},
                t.span({ className: "txt-mono" }, run.workflowName),
                run.version ? t.span({ className: "label m-l-10" }, run.version) : null
              ),
              t.td(
                {},
                t.span({ className: "label " + statusClass(run.status) }, statusLabel(run.status))
              ),
              t.td({}, "" + run.attempts),
              t.td(
                {},
                app.components.formattedDate({
                  value: () => toDisplayDatetime(run.createdAt),
                  short: true
                })
              ),
              t.td(
                {},
                t.span({ className: "txt-mono" }, computeDuration(run.startedAt, run.finishedAt))
              ),
              t.td(
                {},
                !isTerminal(run.status) ? t.button(
                  {
                    type: "button",
                    className: "btn sm danger",
                    onclick: (e) => {
                      e.stopPropagation();
                      cancelRun(run);
                    }
                  },
                  t.span({ className: "txt" }, "Cancel")
                ) : t.span({})
              )
            )
          ) : t.tr(
            {},
            t.td(
              { colSpan: 6 },
              t.span(
                { className: "txt-hint" },
                data.loading ? "Loading..." : data.hasFilters ? "No workflow runs match the current filters." : "No workflow runs yet."
              )
            )
          ))
        ),
        // also kept for a non-default page size, otherwise a size that fits
        // all runs in one page would hide the control to change it back
        () => data.prev || data.next || data.pageSize !== PAGE_SIZES[0] ? t.div(
          { className: "ow-pagination" },
          t.span(
            { className: "txt-hint" },
            () => `Showing ${data.runs.length} run${data.runs.length === 1 ? "" : "s"}`
          ),
          t.div({ className: "flex-fill" }),
          t.span({ className: "txt-hint" }, "Page size"),
          t.div(
            { className: "field ow-page-size" },
            app.components.select({
              options: PAGE_SIZES.map((size) => ({ value: size, label: "" + size })),
              required: true,
              value: () => data.pageSize,
              onchange: (selected) => {
                const size = selected?.[0]?.value;
                if (size && size !== data.pageSize) {
                  setPageSize(size);
                }
              }
            })
          ),
          t.button(
            {
              type: "button",
              className: "btn sm secondary",
              disabled: () => !data.prev || data.loading,
              onclick: () => goToPage(null, data.prev)
            },
            t.i({ className: "ri-arrow-left-s-line" }),
            t.span({ className: "txt" }, "Previous")
          ),
          t.button(
            {
              type: "button",
              className: "btn sm secondary",
              disabled: () => !data.next || data.loading,
              onclick: () => goToPage(data.next, null)
            },
            t.span({ className: "txt" }, "Next"),
            t.i({ className: "ri-arrow-right-s-line" })
          )
        ) : t.div({}),
        t.footer({ className: "page-footer" }, app.components.credits())
      )
    );
  }
  function resolvePageSize(limit) {
    limit = parseInt(limit, 10);
    return PAGE_SIZES.includes(limit) ? limit : PAGE_SIZES[0];
  }

  // web/main.js
  document.head.appendChild(t.link({
    rel: "stylesheet",
    href: app.pb.buildURL("/_/extensions/openworkflow/workflows.css")
  }));
  var collectionsIndex = app.store.headerLinks.findIndex((link) => link.href == "#/collections");
  app.store.headerLinks.splice(collectionsIndex + 1, 0, {
    href: "#/workflows",
    icon: "ri-flow-chart",
    label: "Workflows"
  });
  app.routes.superuserOnly("#/workflows", pageWorkflows);
})();
