# syntax=docker/dockerfile:1

# ---- build the app --------------------------------------------------------
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
COPY internal/ ./internal/
# CGO off gives a static binary that runs on any glibc or musl system.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/meeting_transcriber .

# ---- build the recogniser -------------------------------------------------
# NVIDIA NeMo-Speech.cpp is the recogniser. The published binaries are built
# for a generic baseline, so on ARM they carry none of the dotprod
# instructions this CPU has, and the int8 matmuls that dominate recognition
# run at a fraction of their speed. Building it here for the target CPU is
# worth about 1.6x on an ARM box, and changes nothing in the output.
FROM ubuntu:24.04 AS nemo
ARG NEMO_VERSION=0.2.0
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates cmake g++ git libsentencepiece-dev ninja-build \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone --depth 1 --branch "v${NEMO_VERSION}" \
      https://github.com/NVIDIA/NeMo-Speech.cpp.git . \
    && git submodule update --init llama.cpp
# The build host may not be the machine that runs the image, so the target
# instruction set is pinned rather than taken from the builder. armv8.2-a
# with dotprod covers every ARM server CPU since Ampere, and the x86 flags
# match the baseline the published binaries use.
RUN set -eux; \
    if [ "$(uname -m)" = "aarch64" ]; then \
      scripts/configure.sh cpu-asr \
        -DGGML_NATIVE=OFF \
        -DGGML_CPU_ARM_ARCH=armv8.2-a+dotprod; \
    else \
      scripts/configure.sh cpu-asr \
        -DGGML_NATIVE=OFF \
        -DGGML_AVX=ON -DGGML_AVX2=ON -DGGML_FMA=ON -DGGML_F16C=ON; \
    fi; \
    cmake --build --preset cpu-asr -j"$(nproc)"; \
    cmake --install "build/cpu-asr" --prefix /opt/nemo-speech; \
    /opt/nemo-speech/bin/nemo-speech --version

# ---- runtime --------------------------------------------------------------
FROM ubuntu:24.04

# TARGETARCH is set by buildx and is the machine the image is built for.
ARG TARGETARCH
ARG NEMO_VERSION=0.2.0
ARG OPENCODE_VERSION=1.19.3
# Pinned so a rebuild transcribes the same audio with the same weights.
ARG NEEDLE_REV=d6c7d6a2e9655505e0f8e23339356dd1705900eb
ARG WHISTLE_REV=741118687e1f008f06d1351a402a731274789790

ENV DEBIAN_FRONTEND=noninteractive
ENV HOME=/home/app
ENV PATH=/opt/nemo-speech/bin:/opt/whistle:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# ffmpeg decodes the uploads. The two binaries below are the recogniser and
# the summariser. nemo-speech ships its own shared libraries, so nothing else
# is needed from the system, except the sentencepiece runtime the locally
# built recogniser links against.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl ffmpeg libgomp1 libsentencepiece0 libstdc++6 passwd \
    && rm -rf /var/lib/apt/lists/*

# The recogniser comes from the build stage above, so it carries the
# instructions this CPU can actually run.
COPY --from=nemo /opt/nemo-speech /opt/nemo-speech

# Whistle is the English recogniser: a 1.4 MB engine and 16.9 MB of weights, so
# it is baked in rather than pulled into the cache volume at first start.
RUN set -eux; \
    case "$TARGETARCH" in \
      arm64) whistle_arch=linux-arm64 ;; \
      amd64) whistle_arch=linux-x86_64 ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    mkdir -p /opt/whistle; \
    curl -fsSL -o /opt/whistle/needle \
      "https://huggingface.co/Cactus-Compute/needle3/resolve/${NEEDLE_REV}/${whistle_arch}/needle"; \
    curl -fsSL -o /opt/whistle/whistle.cact \
      "https://huggingface.co/Cactus-Compute/whistle/resolve/${WHISTLE_REV}/whistle.cact"; \
    chmod +x /opt/whistle/needle; \
    ls -l /opt/whistle

# OpenCode runs the free model that writes the summary. It ships a prebuilt
# binary per architecture, so nothing has to be compiled here.
RUN set -eux; \
    case "$TARGETARCH" in \
      arm64) oc_arch=arm64 ;; \
      amd64) oc_arch=x64 ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/oc.tar.gz \
      "https://github.com/anomalyco/opencode/releases/download/v${OPENCODE_VERSION}/opencode-linux-${oc_arch}.tar.gz"; \
    tar xzf /tmp/oc.tar.gz -C /tmp opencode; \
    mv /tmp/opencode /usr/local/bin/opencode; \
    rm -f /tmp/oc.tar.gz; \
    chmod +x /usr/local/bin/opencode /opt/nemo-speech/bin/nemo-speech; \
    nemo-speech --version; \
    opencode --version

COPY --from=build /out/meeting_transcriber /usr/local/bin/meeting_transcriber
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh \
    && useradd --create-home --home-dir /home/app --shell /usr/sbin/nologin app \
    && chown -R app:app /home/app \
    && mkdir -p /data \
    && chown app:app /data

USER app
WORKDIR /home/app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
