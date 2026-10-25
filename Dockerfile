# 服务端容器镜像：状态页经 go:embed 打进二进制，无前端构建。
FROM golang:1.26.8 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/haruka-server ./cmd/haruka-server

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/haruka-server /haruka-server
ENTRYPOINT ["/haruka-server"]
