# 多阶段构建:控制面单进程
FROM golang:1.26 AS build
WORKDIR /src
ENV GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/control-plane ./cmd/control-plane

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/control-plane /control-plane
EXPOSE 8080 8090
USER nonroot:nonroot
ENTRYPOINT ["/control-plane"]
