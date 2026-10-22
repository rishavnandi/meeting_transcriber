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
language chosen on the form
      │
      ├──── English ──── needle (Whistle)   seconds
      │
      └──── Hindi ────── nemo-speech (Nemotron 3.5)
      │
      ▼
opencode  ────  writes the summary from the transcript
```

Four programs do the work, and Go drives them:

| Program | Job |
|---|---|
| `ffmpeg` | decodes the upload |
| `nemo-speech` | recognises Hindi and Hinglish |
| `needle` | recognises English with Cactus Whistle |
| `opencode` | writes the summary with a free model, `opencode/big-pickle` |

## The two speech engines

The upload form has a language selector: **English** or **Hindi / Hinglish**.
It picks the recogniser, because the two engines cannot hear the same thing.

| Recording | Engine | Why |
|---|---|---|
| English only | `whistle` | about fifty times faster than real time |
| Any Hindi | `nemotron-3.5` | handles English and Hindi in the same file |

The choice is manual on purpose. Whistle has no Hindi at all and returns
confident nonsense when it hears some, so a wrong guess would look like a
transcript rather than an error. Its decoder confidence does not separate the
two languages cleanly enough to automate the call.

Whistle is 16.9 MB; Nemotron is over a gigabyte. Whisper would force one
language on the whole file, so on a Hinglish recording it drops half of it.
Nemotron detects the language per utterance and keeps both.

Measured on the target box (4 ARM cores, no GPU):

| Engine | 13 min English meeting | Per hour of audio | FLEURS English | AMI meetings |
|---|---|---|---|---|
| Whistle | 15 s | ~70 s | 12.6% WER | 25.0% WER |
| Nemotron 3.5 / English | 8 m 11 s | ~38 min | 6.8% WER | 9.3% WER |

Whistle makes about twice as many word errors as Nemotron on clean English and
nearly three times as many on real meetings. It also drops names: on the
sample meeting it lost the two people an action item was assigned to. In
exchange it is thirty times faster and uses 74 MB rather than a gigabyte.

The same upload took 66 s end to end with Whistle where it took 421 s before:
16 s of transcription and the rest the summary.

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

## Deploying on a server

The app is deployed on `loki`, an Oracle ARM box in London with four cores and
24 GB of memory. It lives in `/opt/meeting_transcriber`:

```sh
cd /opt/meeting_transcriber
git pull
docker compose up -d --build
```

Docker is set to start on boot and the container has a `restart: unless-stopped`
policy, so the app comes back by itself after a reboot.

The recording that is being transcribed holds under 200 MB of memory for an
English job, and roughly a gigabyte when Nemotron runs on a recording with
Hindi in it. The summary step uses whatever the model needs. The box has room
to spare.

## Configuration

Every setting is an environment variable, set in `docker-compose.yml`.

| Variable | Default | What it does |
|---|---|---|
| `ADDR` | `:8080` | address to listen on |
| `DATA_DIR` | `/data` | where recordings and transcripts are kept |
| `MODEL_MULTILINGUAL` | `nemotron-3.5` | model for recordings containing Hindi |
| `MODEL_ENGLISH` | `whistle` | name shown for the English engine |
| `SUMMARY_MODEL` | `opencode/big-pickle` | model used when the picker is not there |
| `SUMMARY_TIMEOUT` | `15m` | how long the whole summary may take |
| `NEMO_BIN` | `nemo-speech` | path to the recogniser |
| `WHISTLE_BIN` | `needle` | path to the English recogniser |
| `WHISTLE_MODEL` | `/opt/whistle/whistle.cact` | path to its weights |
| `FFMPEG_BIN` | `ffmpeg` | path to ffmpeg |
| `FFPROBE_BIN` | `ffprobe` | path to ffprobe |
| `OPENCODE_BIN` | `opencode` | path to opencode |

## The summary

`opencode run` is called with a free OpenCode Zen model, so there is no API key
and no cost. The list of free models is fetched live and offered as a picker on
the upload form, because the free models come and go: a model has to be both
available to the account and free to be offered. Whichever one is picked is
recorded on the job and shown next to the recording.

The prompt asks for four sections: a summary, the decisions that were made, a
table of action items with owners and due dates, and the questions that were
left open. It also asks for the answer in the same mix of languages the
transcript uses, and tells the model not to invent anything that is not in the
transcript.

A summary that fails does not fail the job. The transcript is the point, so it
is kept and the error is shown next to it.

### Long meetings

The model reads the transcript with a tool that returns at most 2000 lines of a
file, and it does not notice when there is more. Left alone, a meeting of two
hours or more got a summary of only its first part, with nothing to show that
anything was missing.

So a long transcript is split into parts of at most 1500 lines, each part is
summarised on its own, and the part summaries are merged into one. The part
summaries are asked for in the same four sections, so the merge has something
structured to work with. A meeting of an hour or so still needs only one call.

There is a test for this, because the failure is silent and a unit test cannot
catch it:

```sh
SUMMARIZE_TEST=1 go test ./internal/summarize/ -run Long
```

It writes a transcript with a detail on its very last line, summarises it
through the real model, and fails if the summary does not mention that detail.
It is skipped unless `SUMMARIZE_TEST` is set, because it needs a model.

## Layout

```
main.go                 wiring and configuration
internal/store          jobs on disk, one directory each, no database
internal/asr            ffmpeg, the two recognisers
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

The recognisers, `needle` and `nemo-speech`, and ffmpeg have to be on your
`PATH` for `make run` to work. On a Mac:

```sh
brew install ffmpeg
```

## Notes

The summary leaves the box. The transcript does not: the audio is recognised
locally, and only the transcript text is sent to the model that writes the
summary.
