# syntax=docker/dockerfile:1
FROM golang:1.26.5-alpine AS build

ARG SERVICE
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN test -n "$SERVICE" && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gwai "./cmd/${SERVICE}"

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/gwai /gwai
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/gwai"]
