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
	"strings"

	"github.com/ghchinoy/fabulae"

	"cloud.google.com/go/storage"
)

var audioBucketPath string

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

	http.HandleFunc("POST /synthesize", handleSynthesis)
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

	} else { // 2 voice conversation
		outputfiles, err := fabulae.Fabulae(fabulaeRequest.Voice1Name, fabulaeRequest.Voice2Name, fabulaeRequest.Conversation, "", false, "")
		if err != nil {
			http.Error(w, "error synthesizing", http.StatusInternalServerError)
			return
		}
		log.Printf("outputfiles: %s", outputfiles)

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
