FROM golang:1.23 AS builder

WORKDIR /usr/src/app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN go build -o /go/bin/app ./cmd

FROM golang:1.23

WORKDIR /usr/src/app

LABEL "com.datadoghq.ad.logs"='[<LOGS_CONFIG>]'

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates ffmpeg && \
    apt-get clean autoclean && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /go/bin/app /go/bin/app


EXPOSE 8080

CMD ["/go/bin/app"]
