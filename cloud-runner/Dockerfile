ARG GO_VERSION=1.26
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=extra_ca,required=false \
    if [ -s /run/secrets/extra_ca ]; then \
      SSL_CERT_FILE=/run/secrets/extra_ca go mod download; \
    else \
      go mod download; \
    fi
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/agent-harness ./cmd/agent-harness

FROM node:22-bookworm-slim
ARG RAILWAY_CLI_VERSION=5.41.2
ARG CODEX_CLI_VERSION=0.144.1
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates chromium git gh openssh-client python3 \
    && npm install --global "@railway/cli@${RAILWAY_CLI_VERSION}" \
    && npm install --global "@openai/codex@${CODEX_CLI_VERSION}" \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /root/.npm
RUN useradd --create-home --uid 10001 harness
COPY --from=build /out/agent-harness /usr/local/bin/agent-harness
COPY scripts/harnessctl.py /opt/agent-harness/harnessctl.py
USER harness
ENV HOME=/home/harness
ENV PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/usr/bin/chromium
EXPOSE 8080
ENTRYPOINT ["agent-harness"]
CMD ["server"]
