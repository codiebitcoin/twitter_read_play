# ---- 阶段 1：前端 React 构建（front/ → front/dist）----
FROM node:20-alpine AS front
WORKDIR /app/front
COPY front/package.json front/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --legacy-peer-deps
COPY front/ .
RUN --mount=type=cache,target=/root/.npm \
    npm run build

# ---- 阶段 2：Go 构建（go:embed 嵌入 front/dist）----
FROM golang:1.27-alpine AS build

ARG GOPROXY=https://proxy.golang.org,direct
WORKDIR /app
ENV GOPROXY=${GOPROXY}

COPY go.mod go.sum ./
COPY third_party/workshop_inner_sdk ./third_party/workshop_inner_sdk
COPY third_party/livechat ./third_party/livechat
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# 前端产物来自阶段 1（CI / 本地都不需要宿主机装 Node；本地开发用 make build）
COPY --from=front /app/front/dist ./front/dist
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags "-s -w" -o main .

FROM alpine:latest
RUN --mount=type=cache,target=/var/cache/apk \
    apk --no-cache add ca-certificates tzdata curl

ENV TZ=Asia/Shanghai
WORKDIR /app
COPY --from=build /app/main .

RUN mkdir -p /app/data
VOLUME /app/data
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -sf http://localhost:8080/api/health || exit 1

CMD ["./main", "-addr", ":8080", "-data", "/app/data"]
