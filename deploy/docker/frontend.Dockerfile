# Frontend 生产镜像:多阶段构建 React 静态产物,用 nginx-unprivileged 托管。
# 构建上下文为仓库根:
#   docker build -f deploy/docker/frontend.Dockerfile -t ghcr.io/aiops/frontend:latest .
#
# 说明:未改动 frontend 源码;此 Dockerfile 独立置于 deploy/,供 CI 与生产使用。
# 运行时 nginx 站点配置由 K8s ConfigMap(frontend-nginx)挂载覆盖 /etc/nginx/conf.d,
# 镜像内置一份等价默认配置,便于 docker 直跑。

# ---- 构建阶段 ----
FROM node:20-alpine AS build
WORKDIR /app
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# ---- 运行阶段 ----
FROM nginxinc/nginx-unprivileged:1.27-alpine AS runtime
# 非 root(镜像默认 UID 101),监听 8080。
COPY --from=build /app/dist /usr/share/nginx/html
COPY deploy/docker/frontend-nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 8080
