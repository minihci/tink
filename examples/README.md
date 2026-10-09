# examples/

Stacks you can read, copy and edit. They are plain tink YAML: nothing expands them.

| | |
|---|---|
| `edge/` | The shared public edge (a Caddy instance), and two apps that register with it, one behind sign-in and one public on purpose. `tink plan edge/edge.yaml edge/apps.yaml` |
| `opinions/` | One resource in each state against tink's opinions: met, accepted with a reason, and departing. `tink opinions opinions/demo.yaml` |

These are a **spike** for docs/opinion-as-code.md. Not here yet: the Authelia instance itself, because its secrets are files and tink accepts secret references only in
an instance's `environment.*` values.
