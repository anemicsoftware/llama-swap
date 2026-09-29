---
title: Route to remote peers
summary: Expose models from another llama-swap host through a peer connection.
category: guides
tags: [peers, remote, networking]
config_keys: [peers, peers.*.proxy, peers.*.apiKey, peers.*.models, peers.*.discover, peers.*.capabilities]
updated: 2026-09-29
---

# Route to remote peers

Declare a peer with the other llama-swap `proxy` URL and its model list. Its models are addressed as
`peer-name/model-id`, so a peer named `sippy` serving `gemma-4-12B` appears as
`sippy/gemma-4-12B`.

```yaml
peers:
  sippy:
    proxy: http://sippy:8080
    apiKey: ${env.SIPPY_API_KEY}
    models: [gemma-4-12B]
```

The peer must be reachable from this host and its API key must match the remote
server. See `guides/api-integration/api-keys-and-auth` for keeping it out of
the committed file.

## Discover a peer's models and capabilities

Set `discover: true` to have llama-swap ask the peer for its models instead of
listing them by hand. `models` becomes optional and, when present, is added to
what the peer reports.

```yaml
peers:
  sippy:
    proxy: http://sippy:8080
    discover: true
```

llama-swap reads `GET /v1/models` on the peer at startup, on every reload and
every five minutes. When the peer's models or capabilities change, it reloads
the configuration, but only when no local model is running, because a reload
stops running models. If the peer is unreachable, the last good list is kept.

Vision support, tool calling and context size are read from the same listing,
so they appear in this host's `/v1/models` and in the UI. They are only there
if the peer reports them: current llama-swap builds do, older builds list
model IDs only. For those, set them by hand; configured fields win over
discovered ones:

```yaml
peers:
  sippy:
    proxy: http://sippy:8080
    discover: true
    capabilities:
      gemma-4-12B:
        in: [text, image]
        out: [text]
        tools: true
        context: 131072
```

## What goes wrong

- **Models appear but without vision or context size**: the peer's
  `/v1/models` does not include them. Check with
  `curl -s http://sippy:8080/v1/models | jq '.data[0]'`; upgrade the peer or
  set `capabilities` here.
- **"model discovery failed" in the log**: the peer is unreachable or returned
  an error. Requests keep working with the last good list.
- **`tailcat://` peers**: discovery is not supported; list `models` explicitly.
- **Wrong model name**: use the peer's model `id`, not its display `name`.
