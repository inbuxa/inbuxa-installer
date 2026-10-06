#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Build the local AI spam classifier's model from Qwen's official weights, so
# the file that runs is one you made and can check, not a third party's.
#
#   packaging/local-ai/build-model.sh WORKDIR
#
# Downloads Qwen/Qwen3-4B-Instruct-2507 (Apache-2.0) and checks every file
# against the checksums Hugging Face publishes, converts it to GGUF with
# llama.cpp's own converter, and quantizes it to Q4_K_M, the quantization the
# classifier was calibrated with (inbuxa-server's ai-spam-classification
# spec, "Calibration"). Needs docker, curl, python3 and about 20 GB of disk.
#
# Qwen publishes no GGUF of this model; the popular ones are third-party
# conversions. Built with llama.cpp b11160, the result is byte-identical to
# the model production runs, whose SHA-256 is EXPECTED below.
set -euo pipefail

W="${1:?usage: build-model.sh WORKDIR}"
LLAMA=b11160
LLAMA_BIN_SHA=48ece24283876fc3401b737724008c03cbc4c7ba335b6c1aa2a7b6ce2d49e435
MODEL=Qwen/Qwen3-4B-Instruct-2507
NAME=qwen3-4b-instruct-2507
EXPECTED=0f5e5250018ea2e4384e8b440f3a51fd1fff31d6694906e63fca3b5abd8a9a28

mkdir -p "$W/$NAME" "$W/llama-bin"
cd "$W"

echo "== $MODEL, checked against Hugging Face's checksums"
curl -sf "https://huggingface.co/api/models/$MODEL/tree/main" > tree.json
python3 - "$MODEL" "$NAME" <<'PY'
import hashlib, json, subprocess, sys
model, out = sys.argv[1], sys.argv[2]
for f in json.load(open('tree.json')):
    path = f['path']
    if f.get('type') != 'file' or path.startswith('.'):
        continue
    dest = f'{out}/{path}'
    subprocess.run(['curl', '-sfL', '-o', dest,
                    f'https://huggingface.co/{model}/resolve/main/{path}'], check=True)
    oid = (f.get('lfs') or {}).get('oid')
    if oid:
        h = hashlib.sha256()
        with open(dest, 'rb') as fh:
            for block in iter(lambda: fh.read(1 << 22), b''):
                h.update(block)
        if h.hexdigest() != oid:
            sys.exit(f'checksum mismatch: {path}')
        print(f'  ok {path}')
PY

echo "== llama.cpp $LLAMA: converter source and quantizer"
curl -sfL -o "llama-src-$LLAMA.tar.gz" "https://github.com/ggml-org/llama.cpp/archive/refs/tags/$LLAMA.tar.gz"
tar -xzf "llama-src-$LLAMA.tar.gz"
curl -sfL -o "llama-$LLAMA-bin-ubuntu-x64.tar.gz" \
  "https://github.com/ggml-org/llama.cpp/releases/download/$LLAMA/llama-$LLAMA-bin-ubuntu-x64.tar.gz"
echo "$LLAMA_BIN_SHA  llama-$LLAMA-bin-ubuntu-x64.tar.gz" | sha256sum -c
tar -C llama-bin -xzf "llama-$LLAMA-bin-ubuntu-x64.tar.gz"

echo "== convert to GGUF (f16)"
# Runs as the container's root: the converter's dependencies look up the
# user's name, and a host uid with no passwd entry breaks that.
docker run --rm -v "$W":/w -w /w python:3.12-slim sh -c "
  python -m venv /tmp/v &&
  /tmp/v/bin/pip install -q -r llama.cpp-$LLAMA/requirements/requirements-convert_hf_to_gguf.txt \
    --extra-index-url https://download.pytorch.org/whl/cpu >/dev/null &&
  /tmp/v/bin/python llama.cpp-$LLAMA/convert_hf_to_gguf.py $NAME --outtype f16 --outfile $NAME-f16.gguf &&
  chown $(id -u):$(id -g) $NAME-f16.gguf"

echo "== quantize to Q4_K_M"
Q=$(find llama-bin -name llama-quantize -type f | head -1)
LD_LIBRARY_PATH="$(dirname "$Q")" "$Q" "$NAME-f16.gguf" "$NAME-Q4_K_M.gguf" Q4_K_M >/dev/null

GOT=$(sha256sum "$NAME-Q4_K_M.gguf" | cut -d' ' -f1)
echo "== $W/$NAME-Q4_K_M.gguf"
echo "   sha256 $GOT"
if [ "$GOT" = "$EXPECTED" ]; then
  echo "   matches the model production runs"
else
  echo "   differs from production's ($EXPECTED): a different llama.cpp or upstream file change" >&2
  exit 1
fi
