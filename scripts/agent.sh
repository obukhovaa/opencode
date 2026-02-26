#!/bin/bash

# Entrypoint for opencode agent Docker container.
# All arguments are forwarded directly to the opencode binary.
#
# Usage:
#   docker run opencode:latest -p "your prompt" -f json -q
#   docker run opencode:latest -F my-flow -A key1=value1
#   docker run opencode:latest  # interactive mode
#
# Configuration:
#   Mount .opencode.json as a volume:
#     -v /path/to/.opencode.json:/workspace/.opencode.json

cd /workspace || {
	echo "Error: Cannot change to /workspace directory"
	exit 1
}

exec opencode "$@"
