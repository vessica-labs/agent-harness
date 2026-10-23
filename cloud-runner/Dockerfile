FROM golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/vessica-labs/agent-harness/cloud-runner/internal/cli.Version=${VERSION}" \
    -o /out/agent-harness ./cmd/agent-harness

FROM node:22.23.4-trixie-slim@sha256:8eb619e402eaede1748a3c8173bffab391592812e85136452f781741c7242ddf
ARG RAILWAY_CLI_VERSION=5.41.2
ARG CODEX_CLI_VERSION=0.144.1
ARG NPM_VERSION=12.2.1
ARG GH_CLI_VERSION=2.103.0
COPY scripts/install-railway.sh /tmp/install-railway.sh
COPY scripts/install-gh.sh /tmp/install-gh.sh
RUN apt-get update \
    && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends ca-certificates chromium curl git openssh-client python3 \
    && npm install --global "npm@${NPM_VERSION}" \
    && sh /tmp/install-railway.sh "${RAILWAY_CLI_VERSION}" \
    && sh /tmp/install-gh.sh "${GH_CLI_VERSION}" \
    && npm install --global "@openai/codex@${CODEX_CLI_VERSION}" \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /root/.npm /tmp/install-railway.sh /tmp/install-gh.sh
RUN useradd --create-home --uid 10001 harness
COPY --from=build /out/agent-harness /usr/local/bin/agent-harness
COPY scripts/harnessctl.py /opt/agent-harness/harnessctl.py
USER harness
ENV HOME=/home/harness
ENV PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/usr/bin/chromium
EXPOSE 8080
ENTRYPOINT ["agent-harness"]
CMD ["server"]
