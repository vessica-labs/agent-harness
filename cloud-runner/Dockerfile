FROM golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/vessica-labs/agent-harness/cloud-runner/internal/cli.Version=${VERSION}" \
    -o /out/agent-harness ./cmd/agent-harness

FROM node:22.23.4-bookworm-slim@sha256:de170b39439eb0e5a3444ad7d60b015614a381e10fecae2e30ec9e735122f38d
ARG RAILWAY_CLI_VERSION=5.41.2
ARG CODEX_CLI_VERSION=0.144.1
ARG NPM_VERSION=12.2.1
COPY scripts/install-railway.sh /tmp/install-railway.sh
RUN apt-get update \
    && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends ca-certificates chromium curl git gh openssh-client python3 \
    && npm install --global "npm@${NPM_VERSION}" \
    && sh /tmp/install-railway.sh "${RAILWAY_CLI_VERSION}" \
    && npm install --global "@openai/codex@${CODEX_CLI_VERSION}" \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /root/.npm /tmp/install-railway.sh
RUN useradd --create-home --uid 10001 harness
COPY --from=build /out/agent-harness /usr/local/bin/agent-harness
COPY scripts/harnessctl.py /opt/agent-harness/harnessctl.py
USER harness
ENV HOME=/home/harness
ENV PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/usr/bin/chromium
EXPOSE 8080
ENTRYPOINT ["agent-harness"]
CMD ["server"]
