FROM registry.mobrtb.com/library/golang:1.21.5-bullseye

RUN apt-get update && apt-get install -y --no-install-recommends graphviz && \
  apt-get autoremove -y && apt-get autoclean && apt-get clean && rm -rf /var/lib/apt/lists/*
RUN go install github.com/google/pprof@latest

RUN go env -w GO111MODULE=on GOPROXY=http://goproxy.mobrtb.com,direct GONOSUMDB=*

WORKDIR /src

COPY go.mod .
COPY go.sum .
RUN  go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -tags=jsoniter -ldflags="-w -s" -a -o /go/bin/pprof-server .

ENTRYPOINT ["/go/bin/pprof-server"]
