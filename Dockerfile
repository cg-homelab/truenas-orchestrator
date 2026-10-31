FROM alpine:3.21

# uid 1000 is the only supported PUID in M4: the image creates this one user, and ssh (git fetch with
# the deploy key) refuses to run for a uid without a passwd entry. deploy/compose.yaml runs the
# container as "${PUID}:${PGID}", so PUID must be 1000 until the image handles other uids.
RUN apk add --no-cache git openssh-client ca-certificates tzdata \
    && adduser -D -u 1000 orchestrator

COPY dist/linux-amd64/orchestrator /usr/local/bin/orchestrator

USER orchestrator
EXPOSE 8080
ENTRYPOINT ["orchestrator"]
CMD ["serve"]
