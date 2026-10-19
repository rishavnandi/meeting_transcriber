#!/bin/sh
# Entrypoint for the meeting transcriber container.
#
#   pull-models   download the speech recognition models and stop
#   anything else  run the web app
#
# The models are downloaded once into a volume and reused after that. They are
# a gigabyte or so, so baking them into the image would bloat it for no gain.
set -e

if [ "$1" = "pull-models" ]; then
  nemo-speech pull nemotron-3.5
  nemo-speech pull nemotron-en
  # The diariser that works out who spoke when. Small, and the speaker labels
  # on the upload form are on by default.
  nemo-speech pull sortformer
  exit 0
fi

exec /usr/local/bin/meeting_transcriber
