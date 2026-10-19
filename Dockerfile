# syntax=docker/dockerfile:1

# ---- build the app --------------------------------------------------------
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
COPY internal/ ./internal/
# CGO off gives a static binary that runs on any glibc or musl system.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/meeting_transcriber .

# ---- runtime --------------------------------------------------------------
FROM ubuntu:24.04

# TARGETARCH is set by buildx and is the machine the image is built for.
ARG TARGETARCH
ARG NEMO_VERSION=0.2.0
ARG OPENCODE_VERSION=1.19.3

ENV DEBIAN_FRONTEND=noninteractive
ENV HOME=/home/app
ENV PATH=/opt/nemo-speech/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# ffmpeg decodes the uploads. The two binaries below are the recogniser and
# the summariser. nemo-speech ships its own shared libraries, so nothing else
# is needed from the system.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl ffmpeg libgomp1 libstdc++6 passwd \
    && rm -rf /var/lib/apt/lists/*

# NVIDIA NeMo-Speech.cpp runs the Nemotron ASR models, and OpenCode runs the
# free model that writes the summary. Both ship prebuilt binaries per
# architecture, so nothing has to be compiled here.
RUN set -eux; \
    case "$TARGETARCH" in \
      arm64) nemo_arch=aarch64; oc_arch=arm64 ;; \
      amd64) nemo_arch=x86_64; oc_arch=x64 ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/nemo.tar.gz \
      "https://github.com/NVIDIA/NeMo-Speech.cpp/releases/download/v${NEMO_VERSION}/nemo-speech-${NEMO_VERSION}-linux-${nemo_arch}-cpu.tar.gz"; \
    mkdir -p /opt/nemo-speech; \
    tar xzf /tmp/nemo.tar.gz -C /tmp; \
    mv "/tmp/nemo-speech-${NEMO_VERSION}-linux-${nemo_arch}-cpu/"* /opt/nemo-speech/; \
    rmdir "/tmp/nemo-speech-${NEMO_VERSION}-linux-${nemo_arch}-cpu"; \
    rm -f /tmp/nemo.tar.gz; \
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
