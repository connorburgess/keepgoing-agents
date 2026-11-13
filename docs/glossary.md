# Glossary

The words keepgoing uses for its own concepts. New documentation, command output and interface text use these. Older words in configuration and code are listed with what they mean, and are not being renamed.

| Term | Meaning |
| --- | --- |
| **Loop** | One goal being worked on under supervision: a session keepgoing observes, wakes, recovers and bounds until the goal is complete or a person is needed. A loop has one owner at a time. |
| **Lane** | A delegated part of a loop: a child conversation the loop's agent started to work on something in parallel. Today keepgoing knows a loop's lanes only by count and state; it does not supervise or change them. |
| **Route** | Somewhere a turn can run: a model on an account at an effort level, for example a provider's large model on a personal subscription at medium effort. Later a route may also name a place. A loop has an ordered list of routes, most preferred first. |
| **Preferred routes** | The routing rule: work does not stop while any route can take it, so on a limit the loop moves to the next available route at once; and it returns to the most preferred available route at the next turn boundary. Routing uses facts about resources (usage, availability, connectivity, cost class) and never judges the content of the work. Partly implemented; see the [roadmap](roadmap.md#preferred-routes-planned). |
| **Device** | A machine running keepgoing, shown by its own name. Each device supervises the loops that live on it and keeps working when other devices are unreachable. A loop belongs to exactly one device at a time and is always shown with it; see [naming the device](#naming-the-device). |
| **Hop** | Planned, not built: a loop moving from one device to another. The owning device offers it, an enrolled device takes it, ownership moves exactly once, and the new device starts a new conversation with its own logins. See [loops across devices](devices.md). |
| **Relay** | Planned, not built: a program a person may run to carry status and control messages between a panel and devices that cannot reach each other directly. It holds no logins, runs no agents, owns no loops and cannot read what it carries. A request from an agent to its provider never goes through a relay. A relay is not a device. |
| **Command center** | The idea, not a product name: supervision runs on the device where the agents and their logins are, not in a hosted service. Used in design writing. |
| **Run** | One stretch of a loop's work, from when it is started or resumed until it completes. Runs have identifiers, not names; see [run discovery](run-discovery.md). |
| **Hold** | A state in which keepgoing sends nothing until a person acts: paused, stopped, awaiting a person, or completed. |
| **Check** | One pass of the supervisor over a loop: observe, decide, act, record. The command is `keepgoing tick`. |
| **Panel** | The working word for the place a person looks and acts: a local page keepgoing serves to the same machine, with a small presence item in the menu bar or tray. Referred to as "the dock" while it is being designed; the name is not settled. |

## Words to avoid

- **Node.** Readers take it to mean a machine. Use *route* for a model on an account, and *device* for a machine.
- **Server**, for keepgoing itself. Nothing is hosted. The local page is served only to the same machine.

The run-discovery documents use "node" and "link" for the record types of the run graph. That is a storage term and is unrelated to routing.

## Older words still in configuration and code

| Word | Where | Means |
| --- | --- | --- |
| `primary` | `keepgoing.yaml`, status | The loop's first route. |
| `fallbacks`, candidate | `fallbacks.yaml`, `fallbacks` commands | The loop's other routes, in order. |
| target | status, timeline, code | The route a loop is on now. |
| handoff, switch-back | commands, reports, timeline | Moving to a less preferred route, and returning to a more preferred one. |
| child, child thread | status, timeline, code | A lane. |

## What moves, and what never does

Under preferred routes, work moves to wherever it can continue. That is deliberate, and it is bounded so that it cannot spread on its own:

- A loop uses only the routes, accounts and devices a person has configured for it. keepgoing never discovers and adopts new ones, creates accounts, logs in, or copies credentials.
- keepgoing never installs or starts itself on another machine. A loop moves between devices only by an explicit transfer, and has exactly one owner at a time.
- Creating a conversation is opt-in per route, capped per loop and spaced in time, and an attempt with an unknown outcome holds for a person instead of trying again.
- Every move is recorded with its reason, and stop and pause apply before any move.
- A refusal, a policy block or an account restriction is not a limit. It never causes a move to another route; the loop holds for a person. Spreading work across allowances means using accounts the person already holds and may use, never creating or rotating identities to get around a provider's limits.

## Naming the device

- A loop is always shown with its device: `example-app · release prep` on `laptop`. This holds in status, timelines, reports and the panel, including when there is only one device, so that a report read somewhere else still says where the loop is.
- A device is named by its own name everywhere. "This device" means the device whose keepgoing produced the text, and is used only where the reader is certainly on that machine: command output in a terminal there, and the panel when it is opened on the same machine. It may follow the name, as in `laptop (this device)`; it never replaces it.
- Text that may be read elsewhere (reports, a panel opened from another machine or a phone) never says "this device". A phone or browser that is only a panel is not a device and is never called one.

## Names

A name says what the work is, not how it started.

- A loop: `project · goal`, for example `example-app · release prep`.
- A lane: `project · topic`.
- A conversation keepgoing creates when it hands off to a new one keeps the recognisable title described in [fallbacks](fallbacks.md).
