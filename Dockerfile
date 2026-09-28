# Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

# Build stage: runs on the build machine and cross-compiles for each target
# platform (linux/amd64, linux/arm64), so no emulation is needed.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w" -o /out/signal ./cmd/signal
# An unprivileged user for the final image, which has no shell to create it.
RUN echo "nonroot:x:65532:65532:nonroot:/:/sbin/nologin" > /out/passwd && \
    echo "nonroot:x:65532:" > /out/group

# Runtime: scratch, nothing but the static binary, the root certificates
# (for any outgoing TLS) and the user. No shell, no package manager.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/passwd /etc/passwd
COPY --from=build /out/group /etc/group
COPY --from=build /out/signal /signal
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/signal"]
