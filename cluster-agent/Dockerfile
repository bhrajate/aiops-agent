# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache module downloads (no external deps today, but keeps layering correct).
COPY go.mod ./
RUN go mod download

COPY . .
# Static, stripped binary for a scratch runtime.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" \
    -o /out/cluster-agent ./cmd/cluster-agent

# ---- runtime stage ----
FROM scratch AS runtime
# Non-root numeric UID (no user db in scratch).
USER 65532:65532
COPY --from=build /out/cluster-agent /cluster-agent

ENV AIOPS_CLUSTER_AGENT_ADDR=:9100 \
    AIOPS_CLUSTER_ID=prod-cn-1

EXPOSE 9100
ENTRYPOINT ["/cluster-agent"]
