FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -mod=mod -trimpath -ldflags="-s -w" -o /out/experiment .

FROM alpine:3.21
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /out/experiment /app/experiment
COPY --from=builder /src/quizzes /app/quizzes
COPY --from=builder /src/static /app/static
USER app
ENV PORT=8080
EXPOSE 8080
CMD ["/app/experiment"]
