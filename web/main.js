// Workflows superuser UI extension entry point (bundled into ui/main.js).
//
// Adds:
// - the "Workflows" header link (right after "Collections");
// - the #/workflows page with the run-detail and "New Run" side panels.

import { pageWorkflows } from "./pageWorkflows";

document.head.appendChild(t.link({
    rel: "stylesheet",
    href: app.pb.buildURL("/_/extensions/openworkflow/workflows.css"),
}));

const collectionsIndex = app.store.headerLinks.findIndex((link) => link.href == "#/collections");
app.store.headerLinks.splice(collectionsIndex + 1, 0, {
    href: "#/workflows",
    icon: "ri-flow-chart",
    label: "Workflows",
});

app.routes.superuserOnly("#/workflows", pageWorkflows);
