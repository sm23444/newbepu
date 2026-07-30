FROM node:24.18.0-alpine3.23 AS frontend-builder

WORKDIR /web

RUN corepack enable \
    && corepack prepare pnpm@10.28.2 --activate

COPY web/package.json web/pnpm-lock.yaml ./
RUN --mount=type=cache,target=/pnpm/store \
    pnpm config set store-dir /pnpm/store \
    && pnpm install --frozen-lockfile --shamefully-hoist

COPY web/ ./
RUN pnpm run build:prod

FROM golang:1.26.5-alpine3.23 AS builder

ENV GO111MODULE=on
WORKDIR /go/release

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN rm -rf static/secure \
    && mkdir -p static/secure
COPY --from=frontend-builder /web/dist/ ./static/secure/

ARG VERSION=unknown

RUN --mount=type=cache,target=/root/.cache/go-build \
    test -f static/secure/secure.html \
    && MODULE_PATH=$(go list -m) \
    && CGO_ENABLED=0 go build -buildvcs=false -trimpath \
    -ldflags="-X '${MODULE_PATH}/app.Version=${VERSION}' -s -w -buildid=" \
    -o bepusdt ./main

FROM alpine:3.23

ENV TZ=Asia/Shanghai

RUN apk add --no-cache tzdata ca-certificates

COPY --from=builder /go/release/bepusdt /usr/local/bin/bepusdt

RUN ln -fs /usr/share/zoneinfo/Asia/Shanghai /etc/localtime

EXPOSE 8080
ENTRYPOINT ["bepusdt"]
CMD ["start"]
