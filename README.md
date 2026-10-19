# Meeting transcriber

Drop a meeting recording on the web page, get a transcript and a summary back.
It runs entirely on your own box: the audio is decoded, transcribed and
summarised there, and nothing is stored anywhere else.

Built for English and Hindi, and for the Hinglish mix that Indian business
meetings actually use.

## How it works

```
browser upload
      │
      ▼
Go + htmx  ────  saves the recording, queues the job
      │
      ▼
ffmpeg    ────  decodes the upload to 16 kHz mono WAV
      │
      ▼
nemo-speech ───  samples three windows to work out the language,
      │            then transcribes the whole file
      ▼
opencode  ────  writes the summary from the transcript
```

Three programs do the work, and Go drives them:

| Program | Job |
|---|---|
| `ffmpeg` | decodes the upload and cuts the sample windows |
| `nemo-speech` | recognises the audio with NVIDIA's Nemotron ASR models |
| `opencode` | writes the summary with a free model, `opencode/big-pickle` |

## The two speech models

The app picks the recogniser per recording, based on what the audio actually
contains. It samples three short windows near the start, the middle and the
end, and looks for Devanagari in the result.

| Recording | Model | Why |
|---|---|---|
| English only | `nemotron-en` | measurably better on English |
| Any Hindi | `nemotron-3.5` | handles English and Hindi in the same file |

This matters more than it sounds. Whisper forces one language on the whole
file, so on a Hinglish recording it drops half of it. Nemotron detects the
language per utterance and keeps both.

Measured on the target box (4 ARM cores, no GPU), same eleven minutes of
audio for both:

| Engine | Time | Per hour of audio | Hindi | English | Mixed EN+HI file |
|---|---|---|---|---|---|
| Nemotron 3.5 | 7 m 04 s | ~38 min | 1–3 errors per clip | 1–4 errors per clip | both languages kept |
| Whisper large-v3-turbo | 22 m 06 s | ~2 h | 8–10 errors, truncates | 0–1 errors per clip | English half dropped, Hindi garbled |

Error counts are against the FLEURS ground truth, over eight Hindi clips and
four English clips.

## Running it

```sh
git clone git@github.com:rishavnandi/meeting_transcriber.git
cd meeting_transcriber
docker compose up -d --build
```

The first start downloads the speech models, which takes a few minutes. After
that it is ready in seconds.

The app is bound to `127.0.0.1:8080` on the host and is not reachable from
the internet. To put it on your tailnet:

```sh
tailscale serve --bg 8080
```

That gives you `https://<host>.<tailnet>.ts.net`.

```sh
make logs     # follow the logs
make down     # stop it, keep the recordings
make reset    # stop it and delete the recordings
```

## Configuration

Every setting is an environment variable, set in `docker-compose.yml`.

| Variable | Default | What it does |
|---|---|---|
| `ADDR` | `:8080` | address to listen on |
| `DATA_DIR` | `/data` | where recordings and transcripts are kept |
| `MODEL_MULTILINGUAL` | `nemotron-3.5` | model for recordings containing Hindi |
| `MODEL_ENGLISH` | `nemotron-en` | model for English only recordings |
| `SUMMARY_MODEL` | `opencode/big-pickle` | model that writes the summary |
| `SUMMARY_TIMEOUT` | `15m` | how long one summary may take |
| `NEMO_BIN` | `nemo-speech` | path to the recogniser |
| `FFMPEG_BIN` | `ffmpeg` | path to ffmpeg |
| `FFPROBE_BIN` | `ffprobe` | path to ffprobe |
| `OPENCODE_BIN` | `opencode` | path to opencode |

## The summary

`opencode run` is called with a free OpenCode Zen model, so there is no API key
and no cost. `big-pickle` is used because it is a free model that stays
available, unlike the other free models that come and go.

The prompt asks for four sections: a summary, the decisions that were made, a
table of action items with owners and due dates, and the questions that were
left open. It also asks for the answer in the same mix of languages the
transcript uses, and tells the model not to invent anything that is not in the
transcript.

A summary that fails does not fail the job. The transcript is the point, so it
is kept and the error is shown next to it.

## Layout

```
main.go                 wiring and configuration
internal/store          jobs on disk, one directory each, no database
internal/asr            ffmpeg, the recogniser, language detection
internal/summarize      opencode run and reading its answer
internal/tasks          the job queue and the pipeline
internal/web            the htmx handlers, templates and styles
```

Job state lives in `DATA_DIR`:

```
jobs/<id>/job.json         status, stage, summary
jobs/<id>/source.mp3       the uploaded recording
jobs/<id>/transcript.json  the recogniser output, with word timings
jobs/<id>/transcript.txt   the same transcript as readable timestamped text
jobs/<id>/summary.md       the generated summary
```

## Working on it

```sh
make check    # go vet and go test
make run      # run it on this machine, without containers
```

The recogniser and ffmpeg have to be on your `PATH` for `make run` to work.
On a Mac:

```sh
brew install ffmpeg
```

## Notes

The summary leaves the box. The transcript does not: the audio is recognised
locally, and only the transcript text is sent to the model that writes the
summary.
