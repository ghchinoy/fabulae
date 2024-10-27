// Copyright 2024 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ghchinoy/fabulae"
	"github.com/ghchinoy/fabulae/babel"
	"github.com/moutend/go-wav"

	"cloud.google.com/go/storage"
	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

var audioBucketPath string

var (
	projectID string
	location  string
	voices    []*texttospeechpb.Voice
)

type FabulaeRequest struct {
	Voice1Name   string `json:"voice1"`
	Voice2Name   string `json:"voice2"`
	Conversation string `json:"conversation"`
}

type FabulaeResponse struct {
	ErrorMessage string   `json:"errormessage,omitempty"`
	OutputFiles  []string `json:"outputfiles"`
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	audioBucketPath = os.Getenv("GCS_AUDIO_BUCKET")
	if audioBucketPath == "" {
		log.Print("missing GCS_AUDIO_BUCKET, GCS destination for generated audio")
		os.Exit(1)
	}

	// Get Google Cloud Project ID from environment variable
	projectID = envCheck("PROJECT_ID", "") // no default
	if projectID == "" {
		log.Fatalf("please set env var PROJECT_ID with google cloud project, e.g. export PROJECT_ID=$(gcloud config get project)")
	}
	// Get Google Cloud Region from environment variable
	location = envCheck("REGION", "us-central1") // default is us-central1

	// get all journey voices
	var err error
	voices, err = babel.ListJourneyVoices()
	if err != nil {
		log.Fatalf("cannot listJourneyVoices: %v", err)
	}
	log.Printf("%d Journey voices", len(voices))

	http.HandleFunc("POST /synthesize", handleSynthesis)
	http.HandleFunc("GET /voices", babel.HandleListVoices)
	http.ListenAndServe(fmt.Sprintf(":%s", port), nil)
}

func handleSynthesis(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "unable to process body", http.StatusInternalServerError)
		return
	}
	if len(body) == 0 {
		http.Error(w, "no content provided", http.StatusBadRequest)
		return
	}
	log.Printf("%s", body)

	log.Print("synthesizing... ")

	var fabulaeRequest FabulaeRequest
	err = json.NewDecoder(bytes.NewReader(body)).Decode(&fabulaeRequest)
	if err != nil {
		http.Error(w, "error decoding Fabulae Request", http.StatusInternalServerError)
		return
	}

	var response FabulaeResponse

	if fabulaeRequest.Voice2Name == "" { // single voice text synthesis (aka speak)
		log.Print("single voice")
		outputfile, err := fabulae.Speak(fabulaeRequest.Voice1Name, fabulaeRequest.Conversation, audioBucketPath)
		if err != nil {
			http.Error(w, "error synthesizing", http.StatusInternalServerError)
			return
		}
		log.Printf("generated audio at: %s", outputfile)
		outputfiles := []string{}
		outputfiles = append(outputfiles, outputfile)
		log.Printf("outputfiles: %s", outputfiles)
		response = FabulaeResponse{"", outputfiles}
		err = moveFilesToAudioBucket(outputfiles)
		if err != nil {
			http.Error(w, "error writing to Storage", http.StatusInternalServerError)
			return
		}

	} else { // two-voice conversation
		outputfiles, err := fabulae.Fabulae(fabulaeRequest.Voice1Name, fabulaeRequest.Voice2Name, fabulaeRequest.Conversation, "", true, "")
		if err != nil {
			http.Error(w, "error synthesizing", http.StatusInternalServerError)
			return
		}
		log.Printf("outputfiles: %s", outputfiles)

		// join
		combinedWavFile := combineWavFiles("new", outputfiles)
		outputfiles = []string{combinedWavFile}

		response = FabulaeResponse{"", outputfiles}
		err = moveFilesToAudioBucket(outputfiles)
		if err != nil {
			http.Error(w, "error writing to Storage", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	//fmt.Fprintf(w, "%s", body)
	err = json.NewEncoder(w).Encode(response)
	if err != nil {
		log.Print(err)
	}
}

// combineWavFiles appends wav files to a single one
func combineWavFiles(title string, audiolist []string) string {
	wavs := []*wav.File{}
	for _, i := range audiolist {
		wavfile := &wav.File{}
		audiofile := filepath.Join(".", i)
		audiobytes, err := os.ReadFile(audiofile)
		if err != nil {
			log.Fatalf("can't read %s: %v", audiofile, err)
		}
		wav.Unmarshal(audiobytes, wavfile)
		wavs = append(wavs, wavfile)
	}
	log.Printf("Samples per sec: %d, Bits per sample: %d, Channels: %d",
		wavs[0].SamplesPerSec(),
		wavs[0].BitsPerSample(),
		wavs[0].Channels(),
	)
	log.Printf("%d wav files", len(wavs))

	// combine all wavs into one
	outputwav, _ := wav.New(wavs[0].SamplesPerSec(), wavs[0].BitsPerSample(), wavs[0].Channels())
	for _, wav := range wavs {
		io.Copy(outputwav, wav)
	}

	file, _ := wav.Marshal(outputwav)

	outputfilename := fmt.Sprintf("%s_%s.wav", title, time.Now().Format("20060102.030405.06"))
	os.WriteFile(outputfilename, file, 0644)

	// delete temp files
	for _, i := range audiolist {
		err := os.Remove(i)
		if err != nil {
			log.Printf("os.Remove: %v", err)
		}
	}

	return outputfilename
}

func moveFilesToAudioBucket(outputfiles []string) error {
	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	parts := strings.Split(audioBucketPath, "/")
	bucketName := parts[0]
	storagePath := strings.Join(parts[1:], "/")

	for _, audiofile := range outputfiles {
		objectName := fmt.Sprintf("%s/%s", storagePath, audiofile)
		f, err := os.Open(audiofile)
		if err != nil {
			log.Printf("unable to open file %s: %v", audiofile, err)
			return err
		}
		defer f.Close()

		log.Printf("writing to %s %s", bucketName, objectName)
		o := client.Bucket(bucketName).Object(objectName)

		o = o.If(storage.Conditions{DoesNotExist: true})

		wc := o.NewWriter(ctx)
		if _, err = io.Copy(wc, f); err != nil {
			return fmt.Errorf("io.Copy: %w", err)
		}
		if err := wc.Close(); err != nil {
			return fmt.Errorf("Writer.Close: %w", err)
		}

		err = os.Remove(audiofile)
		if err != nil {
			return fmt.Errorf("os.Remove: %w", err)
		}
	}

	return nil
}

// envCheck checks for an environment variable, otherwise returns default
func envCheck(environmentVariable, defaultVar string) string {
	if envar, ok := os.LookupEnv(environmentVariable); !ok {
		return defaultVar
	} else if envar == "" {
		return defaultVar
	} else {
		return envar
	}
}
