FROM alpine:3.21

RUN apk add --no-cache git openssh-client ca-certificates tzdata \
    && adduser -D -u 1000 orchestrator

COPY dist/linux-amd64/orchestrator /usr/local/bin/orchestrator

USER orchestrator
EXPOSE 8080
ENTRYPOINT ["orchestrator"]
CMD ["serve"]
