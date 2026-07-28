# syntax=docker/dockerfile:1

# ---- 构建阶段 ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# 缓存模块下载(client-go 及其传递依赖)。
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# 编译为静态、去符号的二进制,以便在 scratch 运行时中运行。
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" \
    -o /out/cluster-agent ./cmd/cluster-agent

# ---- 运行阶段 ----
FROM scratch AS runtime
# 使用非 root 的数字 UID(scratch 镜像里没有用户数据库)。
USER 65532:65532
COPY --from=build /out/cluster-agent /cluster-agent

ENV AIOPS_CLUSTER_AGENT_ADDR=:9100 \
    AIOPS_CLUSTER_ID=prod-cn-1

EXPOSE 9100
ENTRYPOINT ["/cluster-agent"]
