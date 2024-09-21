# fabulae: cli for creating audio from text conversations


Given a 2 person, turn-by-turn, one turn per line text file, generate an audio of the conversation. Lines may contain [SSML](https://cloud.google.com/text-to-speech/docs/ssml). 

Uses [Google Cloud Text-to-Speech](https://cloud.google.com/text-to-speech/docs)'s [Go SDK](https://pkg.go.dev/cloud.google.com/go/texttospeech/apiv1).

* Authenticate to your GCP account
* Generate Audio
* Audio Analysis

## Authenticate to your GCPaccount

Impersonation with a service account

```
# service account creation
export PROJECT_ID=[your project id here]
export SERVICE_ACCOUNT=sa-tts
export SERVICE_ACCOUNT_EMAIL=${SERVICE_ACCOUNT}@${PROJECT_ID}.iam.gserviceaccount.com
gcloud iam service-accounts create ${SERVICE_ACCOUNT} \
  --display-name "tts generator"
gcloud projects add-iam-policy-binding ${PROJECT_ID} --member \
  serviceAccount:${SERVICE_ACCOUNT_EMAIL} \
  --role=roles/speech.admin

# impersonate
gcloud auth application-default login --impersonate-service-account=$SERVICE_ACCOUNT_EMAIL
```

## Generate Audio

```
fabulae conversation.txt
```

## Audio analysis

### Determine audio length

Use `ffprobe` to get length of conversation

```
ffprobe -i conversation.wav -show_entries format=duration -v quiet -of csv="p=0" -sexagesimal
```



## Service

The `service` directory contains a HTTP service that will upload the generated file to a GCS bucket

The GCS bucket must be specified in an environment variable, without the `gs://` prefix, or trailing `/`

```
export GCS_AUDIO_BUCKET=my-bucket/audio-folder
```