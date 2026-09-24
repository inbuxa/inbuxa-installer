# Local AI for spam filtering

The model behind inbuxa's AI spam classifier, run on the same machine as the
mail server, as production runs it. Nothing here is used by the installer
yet: it's the reference for its bare-metal mode, and the record of what runs.

The classifier itself is in inbuxa-server (the `ai-spam-classification`
spec). It's off until an administrator turns it on in inbuxa Admin, under
Settings › Spam Filter › Local AI. The model's opinion is one bounded signal
among many (at most +2 points by default), and a slow or missing model never
holds up mail.

## What runs

| Piece | What |
|---|---|
| Server | llama.cpp `b11160`, the CPU build `llama-b11160-bin-ubuntu-x64.tar.gz` (SHA-256 `48ece242…e435`), in `/opt/llama/b11160` |
| Model | Qwen3 4B Instruct 2507 (Apache-2.0), quantized to Q4_K_M: `/opt/llama/models/qwen3-4b-instruct-2507-Q4_K_M.gguf`, SHA-256 `0f5e5250018ea2e4384e8b440f3a51fd1fff31d6694906e63fca3b5abd8a9a28` |
| Service | [`inbuxa-llm.service`](inbuxa-llm.service): in the mail network namespace, on its loopback `127.0.0.1:8080` only, 4 cores and 8 GB at most, 4 slots of 4096 tokens |

The mail server's model entry (`x:AiModel`) points at
`http://127.0.0.1:8080/v1/chat/completions` with the model name
`qwen3-4b-instruct-2507`, which is the service's `--alias`. The address is
the mail namespace's own loopback, so message text never leaves the machine.

Measured on production (CPU only): about 1.2 s per message once warm,
4 s for the first; about 4.3 GB of memory.

## The model, built from source

Qwen publishes no GGUF of this model, and the popular ones are third-party
conversions. [`build-model.sh`](build-model.sh) makes it from Qwen's official
weights instead: it checks each file against Hugging Face's published
checksums, converts with llama.cpp's own converter, quantizes to Q4_K_M, and
compares the result with production's SHA-256.

```bash
packaging/local-ai/build-model.sh ~/ai-models
```

## Installing it by hand

On a Debian 13 host whose mail server runs in the `mail` namespace
(`/run/netns/mail`, from `mail-netns.service`):

```bash
apt-get install libgomp1        # llama.cpp's OpenMP runtime
mkdir -p /opt/llama/models
curl -fLO https://github.com/ggml-org/llama.cpp/releases/download/b11160/llama-b11160-bin-ubuntu-x64.tar.gz
echo "48ece24283876fc3401b737724008c03cbc4c7ba335b6c1aa2a7b6ce2d49e435  llama-b11160-bin-ubuntu-x64.tar.gz" | sha256sum -c
mkdir -p /opt/llama/b11160 && tar -C /opt/llama/b11160 -xzf llama-b11160-bin-ubuntu-x64.tar.gz
install -m 0644 qwen3-4b-instruct-2507-Q4_K_M.gguf /opt/llama/models/
install -m 0644 packaging/local-ai/inbuxa-llm.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now inbuxa-llm
ip netns exec mail curl -s http://127.0.0.1:8080/health   # {"status":"ok"}
```

Port 8080 is the mail namespace's own loopback, not the host's, so it can't
collide with anything outside the namespace. That matters because the
installer's container install binds the webmail to the host's
`127.0.0.1:8080`.

A host without the mail namespace drops `NetworkNamespacePath`,
`After=mail-netns.service` and `Requires=mail-netns.service` from the unit,
and the service then listens on the host's loopback. There, pick another port
(`--port` in the unit) if the installer has set the host up, since the webmail
already holds 8080, and change the address in inbuxa Admin's Local AI page
(or the model's `url`) to match.

## Undoing it

`systemctl disable --now inbuxa-llm`. The classifier then fails fast on every
message and mail flows as before. Turn the classifier off in inbuxa Admin to
stop it trying.
