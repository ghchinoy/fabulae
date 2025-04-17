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
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ghchinoy/fabulae/babel"
	fabulae "github.com/ghchinoy/fabulae/core"
	"github.com/moutend/go-wav"

	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

var audioBucketPath string

var (
	projectID string
	location  string
	voices    []*texttospeechpb.Voice
)

type FabulaeRequest struct {
	PDFURL       string `json:"pdf_url"`
	Voice1Name   string `json:"voice1"`
	Voice2Name   string `json:"voice2"`
	Conversation string `json:"conversation"`
}

type FabulaeResponse struct {
	ErrorMessage  string   `json:"errormessage,omitempty"`
	OutputFiles   []string `json:"outputfiles"`
	AudioURI      string   `json:"audio_uri"`
	TranscriptURI string   `json:"transcript_uri"`
	Title         string   `json:"title"`
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

	http.HandleFunc("POST /synthesize", handleSynthesis)
	http.HandleFunc("GET /voices", babel.HandleListVoices)
	http.HandleFunc("POST /babel", babel.HandleSynthesis)
	if err := http.ListenAndServe(fmt.Sprintf(":%s", port), nil); err != nil {
		log.Fatalf("error starting service: %v", err)
	}
}

// handleSynthesis handles the Fabulae conversation creation and synthesis
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
	var response FabulaeResponse

	err = json.NewDecoder(bytes.NewReader(body)).Decode(&fabulaeRequest)
	if err != nil {
		http.Error(w, "error decoding Fabulae Request", http.StatusInternalServerError)
		return
	}

	storytype := "podcast"

	if fabulaeRequest.PDFURL != "" {
		// obtain the PDF & store the PDF
		gcsURI, err := addPDFSourceToGCS(fabulaeRequest.PDFURL)
		if err != nil {
			log.Printf("error addPDFSourceToGCS: %v", err)
			http.Error(w, "error obtaining source", http.StatusInternalServerError)
			return
		}
		// create conversation
		fabulaeRequest.Conversation, err = createConversationFromPDFURL(gcsURI)
		if err != nil {
			log.Printf("error createConversationFromPDFURL: %v", err)
			http.Error(w, "error creating conversation", http.StatusInternalServerError)
			return
		}

		response.Title = getTitleOfDocument(gcsURI)

		// default voices if there are none
		if fabulaeRequest.Voice1Name == "" {
			fabulaeRequest.Voice1Name = "en-US-Chirp3-HD-Charon"
			fabulaeRequest.Voice2Name = "en-US-Chirp3-HD-Leda"
		}
	}

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
		response.OutputFiles = outputfiles
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
		response.OutputFiles = outputfiles
		response.AudioURI = outputfiles[0]

		// transcript
		filetitle := removeNonAlphanumerics(response.Title)
		transcriptfilename := fmt.Sprintf("%s-%s_%s_transcript.txt",
			storytype,
			filetitle,
			time.Now().Format("20060102.030405.06"),
		)
		os.WriteFile(transcriptfilename, []byte(fabulaeRequest.Conversation), 0644)
		response.TranscriptURI = transcriptfilename

		outputfiles = append(outputfiles, transcriptfilename)
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
