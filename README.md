# fabulae: cli for creating audio from text conversations

Fabulae creates an audio conversation from a given PDF URL or a text transcript.

The primary example is a command-line tool, `fabulae`, but there's also a webservice.


## Prerequisites

* Google Cloud Project with Cloud Services enabled
* [Go](https://go.dev/doc/install)
* Environment variable for your Project ID
* Fabulae CLI

```
# enable services
gcloud services enable texttospeech.googleapis.com aiplatform.googleapis.com

# set project ID environment variable
export PROJECT_ID=$(gcloud config get project)
```

```
# install the fabluae cli
go install github.com/ghchinoy/fabulae/cli@latest
```

## Try it

```
# try with the audiolm paper
fabulae-cli --pdf-url https://arxiv.org/pdf/2209.03143
```

Listen with your favorite audio player. 

On OS X, you can use `afplay`, e.g. `afplay 20240921.045413.24.wav`

On Linux, you can use `play` if you have sox installed, `play 20240921.045413.24.wav`


![](./assets/fabulae-usage.gif)


## Service

The `service` directory contains a HTTP service that will upload the generated file to a GCS bucket

The GCS bucket must be specified in an environment variable, without the `gs://` prefix, or trailing `/`

```
export GCS_AUDIO_BUCKET=my-bucket/audio-folder
```

# Related

For the parent solution, see [GenMedia Studio](https://github.com/GoogleCloudPlatform/vertex-ai-creative-studio)

# Disclaimer

This is not an officially supported Google product.
