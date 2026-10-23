##################################
# 第一阶段：构建GO可执行文件
##################################

# 使用官方的 Go 基础镜像作为构建环境
ARG GO_VERSION=1.26.10
# 运行时基础镜像：无 shell、自带 CA 证书，:nonroot 标签默认以 UID 65532 运行
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot
FROM golang:${GO_VERSION}-alpine AS builder

ARG SERVICE_NAME=admin
ARG APP_VERSION=1.0.0
# 构建期依赖代理：默认国内镜像优先，回退官方代理与 direct；
# 可按构建环境用 --build-arg GOPROXY=... 覆盖。校验仍由 go.sum 保证。
ARG GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct

# 设置工作目录
WORKDIR /src

# 复制项目源代码到工作目录
COPY . /src

# 固定版本的领域模块：按上面的代理链下载，校验由 go.sum 保证。
RUN go mod download

# 编译可执行文件（使用WORKDIR和相对路径，而不是cd）
# -trimpath + "-s -w" 去掉符号表与 DWARF，镜像体积显著下降；排障需按 build id 对应源码。
RUN CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w -X main.version=$APP_VERSION" \
    -o /src/bin/${SERVICE_NAME}-server ./app/${SERVICE_NAME}/service/cmd/server/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o /src/bin/admin ./app/admin/service/cmd/admin/

# 复制配置文件到统一目录
RUN mkdir -p /src/bin/configs && \
    if [ -d "/src/app/${SERVICE_NAME}/service/configs" ]; then \
      cp -r /src/app/${SERVICE_NAME}/service/configs/* /src/bin/configs/ 2>/dev/null || true; \
    fi

##################################
# 第二阶段：创建最终的运行时镜像
##################################

# 一次性运维工具镜像：只含 admin CLI，只连 PostgreSQL，用于 init / check / sync-apis。
FROM ${RUNTIME_IMAGE} AS runtime-admin

WORKDIR /app

COPY --from=builder /src/bin/admin /app/bin/admin

# Preserve the upstream copyright and permission notice in distributed images.
COPY --from=builder /src/THIRD_PARTY_NOTICES.md /app/THIRD_PARTY_NOTICES.md

# 无子命令时打印 usage 并以非 0 退出；实际用法：admin init|check|sync-apis
CMD ["/app/bin/admin"]

# 服务镜像：最后一个阶段即默认 target（不指定 --target 时构建它）。
FROM ${RUNTIME_IMAGE} AS runtime-server

ARG SERVICE_NAME=admin

WORKDIR /app

# 从第一阶段的构建结果中复制可执行文件到当前工作目录
COPY --from=builder /src/bin/${SERVICE_NAME}-server /app/bin/server

# 拷贝配置文件
COPY --from=builder /src/bin/configs/ /app/configs/

# Preserve the upstream copyright and permission notice in distributed images.
COPY --from=builder /src/THIRD_PARTY_NOTICES.md /app/THIRD_PARTY_NOTICES.md

# 设置容器启动时执行的命令
CMD ["/app/bin/server", "-c", "/app/configs"]
