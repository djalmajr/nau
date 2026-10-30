# Adapters

Nau is a universal messaging and registry core. The core is neutral: it owns the durable outbox, the routing and the job bookkeeping, and it reaches the outside world only through its boundary adapters. There are four of them, one per boundary.

## Core

The neutral core is the `nau` CLI and its state directory: the durable outbox with its gapless `seq`, the dispatcher-side routing and the local job bookkeeping. The core has no boundary of its own; every boundary belongs to one adapter, so adding an adapter does not change the core.

## Hermes

Hermes is the remote job dispatcher boundary. On the node it is the two-way bridge: the node records job events, job-workspace events and decisions in the outbox, the dispatcher can pull them with `nau outbox`, and the node can push them to a configured dispatcher URL with a per-user API key. Status: implemented in part — the pull of the outbox and the push client are implemented on the node, and the dispatcher-side receiver does not exist yet; until it does, the node works standalone through the pull channel.

## Herdr

Herdr is the node boundary: the forwarding of the dispatcher-facing job subcommands to the `soho` public CLI (the backend). `soho` stays an independent project; Nau uses only its public CLI. Status: implemented.

## Grok Bot

Planned — not implemented. Grok Bot is a planned boundary adapter; it has no code, commands or configuration yet, and the documentation never describes commands, keys or endpoints for it.

## OpenAI Dots

Planned — not implemented. OpenAI Dots is a planned boundary adapter; it has no code, commands or configuration yet, and the documentation never describes commands, keys or endpoints for it.
