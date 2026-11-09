# keepgoing for BB

A thin [BB](https://getbb.app) plugin for [keepgoing](../../README.md). It shows keepgoing's state in the context card of every thread a loop supervises, with a Pause or Resume button. It also adds a `bb keepgoing` command. All decisions stay in keepgoing; the plugin only runs the `keepgoing` CLI and renders its JSON.

## Install

```sh
cd integrations/bb-plugin-keepgoing
npm install
bb plugin build
bb plugin install .
bb plugin config keepgoing set keepgoingPath /absolute/path/to/keepgoing
bb plugin config keepgoing set configs "/absolute/path/to/keepgoing.yaml"
```

`configs` takes one `keepgoing.yaml` path per line. Only those loops are shown or controlled. A path sent from the browser is checked against this list, so the plugin never runs keepgoing against an arbitrary file. The plugin needs a keepgoing build whose `status --json` includes `primaryAgent` and `targetAgent`, which is any build after 0.1.0.

## What it shows

A supervised thread's context card gets one row:

- `keepgoing: running`: the loop is enabled and this thread is the one it supervises right now.
- `keepgoing: watching (<target>)`: this is the loop's primary, but a fallback has taken over.
- `keepgoing: paused`, `stop requested`, `awaiting user`, or `disabled`: the loop's control state.

The tooltip carries the loop's note, the last time something was sent, and the fallback health summary.

## Commands

```sh
bb keepgoing status
bb keepgoing pause <loop>
bb keepgoing resume <loop>
bb keepgoing stop <loop>
```
