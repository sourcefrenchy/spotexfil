# spotexfil payload type for Mythic (v3, mythic_container PyPi package).
#
# Base image: python3 + go toolchain + latest mythic-container package.
# Poseidon (MythicAgents/poseidon) uses the sibling `mythic_go_macos:latest`
# image because its container is written in Go; zippy and other python-based
# containers use `mythic_python_go:latest`. We pin the same family. If you
# need reproducibility, pin an explicit tag (e.g. 1.2.22) instead of `latest`.
FROM itsafeaturemythic/mythic_python_go:latest

# The go.mod of the agent requires a newer Go than the base image may ship
# (go 1.26.x). GOTOOLCHAIN=auto lets `go` fetch the required toolchain on
# demand; pre-warm it now so payload builds work without network access.
ENV GOTOOLCHAIN=auto
RUN mkdir -p /tmp/toolchain-warm \
    && cd /tmp/toolchain-warm \
    && go mod init warm \
    && go mod edit -go=1.26.0 \
    && go mod download \
    ; cd / && rm -rf /tmp/toolchain-warm

WORKDIR /Mythic/

# Payload type container code (python builder + command definitions).
# NOTE: the agent's Go module (<repo>/go) must be staged into ./agent_code
# BEFORE `docker build` (see ./install.sh and README.md); Mythic also
# bind-mounts this directory at /Mythic at runtime.
COPY [".", "."]

CMD ["python3", "main.py"]
