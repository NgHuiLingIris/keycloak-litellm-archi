# AICOE MaaS Local Stack

This Docker Compose stack runs Keycloak, Ollama, LiteLLM, and a patched Bifrost image.

## Local Model Setup

Ollama must have the model pulled before LiteLLM or Bifrost can use it.

The current working local model is:

```text
llama3.2:1b
```

Check installed Ollama models:

```bash
docker-compose exec -T ollama ollama list
```

Pull the current test model:

```bash
docker-compose exec -T ollama ollama pull llama3.2:1b
```

## LiteLLM Models

LiteLLM is available at:

```text
http://localhost:4000
```

The current `litellm_config.yaml` exposes these model groups:

```text
tinyllama    -> ollama/llama3.2:1b
llama3.2:1b  -> ollama/llama3.2:1b
```

`tinyllama` is kept as a compatibility alias, but it currently routes to the installed Ollama model `llama3.2:1b`.

Test LiteLLM with the alias:

```bash
curl -sS http://localhost:4000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-1234" \
  -d '{"model":"tinyllama","messages":[{"role":"user","content":"ping"}]}'
```

Test LiteLLM with the direct model group:

```bash
curl -sS http://localhost:4000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-1234" \
  -d '{"model":"llama3.2:1b","messages":[{"role":"user","content":"ping"}]}'
```

If you use a generated LiteLLM key instead of `sk-1234`, make sure that key is allowed to call the selected model group.

## Bifrost Models

Bifrost is available at:

```text
http://localhost:4001
```

For Ollama through Bifrost, include the provider prefix in the request model:

```text
ollama/llama3.2:1b
```

Test Bifrost without a virtual key:

```bash
curl -sS http://localhost:4001/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"ollama/llama3.2:1b","messages":[{"role":"user","content":"ping"}]}'
```

Test Bifrost with a virtual key:

```bash
curl -sS http://localhost:4001/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <BIFROST_VIRTUAL_KEY>" \
  -d '{"model":"ollama/llama3.2:1b","messages":[{"role":"user","content":"ping"}]}'
```

If a Bifrost virtual key returns `model_blocked`, edit the key in:

```text
http://localhost:4001/workspace/governance/virtual-keys
```

Allow this model name:

```text
llama3.2:1b
```

Bifrost strips the `ollama/` provider prefix before checking the virtual key model allowlist.

## Common Model Errors

`model "llama3" not found, try pulling it first`

Ollama does not have that exact model installed. Pull it or use the installed model:

```bash
docker-compose exec -T ollama ollama pull llama3.2:1b
```

`model 'tinyllama' not found`

LiteLLM is asking Ollama for `tinyllama`. In this repo, `tinyllama` should be an alias to `ollama/llama3.2:1b`. Restart LiteLLM after editing `litellm_config.yaml`:

```bash
docker-compose restart litellm
```

`Invalid model name passed in model=None`

The request body did not reach LiteLLM. Usually this is a shell command issue, such as missing `\` before the `-d` line.

Use a compact command to avoid that:

```bash
curl -sS http://localhost:4000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-1234" \
  -d '{"model":"tinyllama","messages":[{"role":"user","content":"ping"}]}'
```
