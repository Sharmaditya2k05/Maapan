# syntax=docker/dockerfile:1.7
#
# go.mod has `replace github.com/maapan/mool => ../mool`, so the shared library is
# passed in as a named build context and copied next to the service source:
#
#   docker buildx build --build-context mool=../mool -t aadesh .
#
# Cloud Build clones the mool repo into .mool and passes --build-context mool=.mool
# (see cloudbuild.yaml). docker-compose uses `additional_contexts`.

FROM golang:1.25-alpine AS build
WORKDIR /src/svc
COPY --from=mool . /src/mool
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/server

# distroless/static has CA certs and tzdata, runs as non-root, ~2 MB.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app"]
