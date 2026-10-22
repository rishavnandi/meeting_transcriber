#!/bin/sh
# Entrypoint for the meeting transcriber container.
#
#   pull-models   download the speech recognition models and stop
#   anything else  run the web app
#
# The Hindi model is downloaded once into a volume and reused after that. The
# English engine's 17 MB is baked into the image.
set -e

if [ "$1" = "pull-models" ]; then
  nemo-speech pull nemotron-3.5
  exit 0
fi

exec /usr/local/bin/meeting_transcriber
