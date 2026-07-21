# ---- Stage 1: Build TDLib + Go binary ----
FROM ubuntu:22.04 AS builder

# Install build dependencies
RUN apt-get update && apt-get install -y \
    build-essential \
    cmake \
    gperf \
    libssl-dev \
    zlib1g-dev \
    libreadline-dev \
    git \
    ca-certificates \
    golang-go \
    && rm -rf /var/lib/apt/lists/*

# Build TDLib
WORKDIR /tmp
RUN git clone --depth=1 --branch v1.8.41 https://github.com/tdlib/td.git td-src \
    && mkdir td-src/build && cd td-src/build \
    && cmake -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local .. \
    && cmake --build . -j$(nproc) \
    && cmake --install .

# Build Go binary
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o /app/chat-summary-bot .

# ---- Stage 2: Runtime ----
FROM ubuntu:22.04 AS runtime

RUN apt-get update && apt-get install -y \
    ca-certificates \
    libssl-dev \
    zlib1g-dev \
    libreadline-dev \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /usr/local/lib/libtdjson.so* /usr/local/lib/
COPY --from=builder /usr/local/lib/libtdjson_static* /usr/local/lib/ 2>/dev/null || true
COPY --from=builder /app/chat-summary-bot /app/chat-summary-bot
COPY --from=builder /app/etc/config.yaml.sample /app/etc/config.yaml.sample

RUN ldconfig

WORKDIR /app

CMD ["/app/chat-summary-bot", "-f", "/app/etc/config.yaml"]
