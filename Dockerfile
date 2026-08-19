FROM golang:1.25 AS backend
ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -o /out/ocr-review-bot ./cmd/ocr-review-bot

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates wget python3 python3-venv && rm -rf /var/lib/apt/lists/*
RUN python3 -m venv /opt/code-review-graph && /opt/code-review-graph/bin/pip install --no-cache-dir --index-url https://mirrors.aliyun.com/pypi/simple/ --trusted-host mirrors.aliyun.com "code-review-graph==2.3.7"
ENV PATH="/opt/code-review-graph/bin:${PATH}"
WORKDIR /app
COPY --from=backend /out/ocr-review-bot /usr/local/bin/ocr-review-bot
COPY build/config-docker.json /app/config.json
VOLUME ["/data"]
ENV OCR_BOT_CONFIG=/app/config.json
EXPOSE 8080
ENTRYPOINT ["ocr-review-bot"]
