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
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"cloud.google.com/go/vertexai/genai"
	"github.com/ghchinoy/fabulae"

	"github.com/moutend/go-wav"
)

var (
	conversationfile       string
	pdfurl                 string
	configfile             string
	voice1name, voice2name string
	striptags              string
	turnbyturn             bool
	projectID              string
	location               string
	modelName              string
	assetdir               string
)

//go:embed prompts/*.tpl
var promptTemplates embed.FS

func init() {
	flag.StringVar(&conversationfile, "conversationfile", "", "path to transcript")
	flag.StringVar(&pdfurl, "pdf-url", "", "URL for PDF")
	flag.StringVar(&modelName, "model", "gemini-1.5-flash", "generative model name")
	flag.StringVar(&assetdir, "assetdir", ".", "output folder")

	flag.StringVar(&configfile, "config", "", "path to JSON config file")
	flag.StringVar(&voice1name, "voice1", "en-US-Journey-D", "voice 1")
	flag.StringVar(&voice2name, "voice2", "en-US-Journey-F", "voice 2")
	flag.StringVar(&striptags, "strip", "AGENT,CUSTOMER", "particpant labels to split")
	flag.BoolVar(&turnbyturn, "turn-by-turn", true, "output each turn as a wav")
	flag.Parse()
}

func main() {

	// Get Google Cloud Project ID from environment variable
	projectID = envCheck("PROJECT_ID", "") // no default
	if projectID == "" {
		log.Fatalf("please set env var PROJECT_ID with google cloud project, e.g. export PROJECT_ID=$(gcloud config get project)")
	}
	// Get Google Cloud Region from environment variable
	location = envCheck("REGION", "us-central1") // default is us-central1
	// flag guard
	if conversationfile == "" {
		if pdfurl == "" {
			log.Fatalln("Must have one of either a transcript or a pdf-url source")
		}
	}

	var conversation string

	if pdfurl != "" {
		var err error
		conversation, err = createConversationFromPDFURL(pdfurl)
		if err != nil {
			log.Printf("unable to create conversation from url %s: %v", pdfurl, err)
			os.Exit(1)
		}
	} else {
		// conversation
		// assume text (different speakers, turn-by-turn, one line each)
		// if not, use JSON conversation format
		//if len(flag.Args()) < 1 {
		//	log.Printf("please provide path to conversation file")
		//	os.Exit(1)
		//}
		conversationfile := flag.Arg(0)
		convbytes, err := os.ReadFile(conversationfile)
		if err != nil {
			log.Printf("couldn't find %s: %s", conversationfile, err.Error())
			os.Exit(1)
		}
		conversation = string(convbytes)
	}

	// create file name for conversation audio output
	outputfilename := fmt.Sprintf("%s_%s.wav",
		strings.Split(conversationfile, ".")[0],
		time.Now().Format("20060102.030405.06"),
	)

	audiofiles, err := fabulae.Fabulae(voice1name, voice2name, conversation, outputfilename, turnbyturn, striptags)
	if err != nil {
		log.Fatalf("error in Fabulae: %v", err)
	}
	output := combineWavFiles(audiofiles)
	log.Printf("combined: %s", output)

}

// combineWavFiles appends wav files to a single one
func combineWavFiles(audiolist []string) string {
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

	outputfilename := fmt.Sprintf("%s.wav", time.Now().Format("20060102.030405.06"))
	os.WriteFile(outputfilename, file, 0644)

	// delete temp files
	for _, i := range audiolist {
		err := os.Remove(i)
		if err != nil {
			log.Printf("os.Remove: %w", err)
		}
	}

	return outputfilename
}

func createConversationFromPDFURL(pdfurl string) (string, error) {
	//pdfcontent, err := retrievePDFContent(pdfurl)
	//if err != nil {
	//	return "", err
	//}

	conversation, err := generateConversationFrom(projectID, location, modelName, pdfurl)
	if err != nil {
		return "", err
	}

	return conversation, nil
}

// retrievePDFContent given an URL, retrieve the data at that URL
func retrievePDFContent(pdfurl string) (string, error) {
	// TODO guard against non-PDF data
	var buf bytes.Buffer
	req, err := http.NewRequest("GET", pdfurl, nil)
	if err != nil {
		return "", err
	}

	client := &http.Client{}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if err := res.Write(&buf); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// generateConversationFrom creates a conversation using the provided file URL
func generateConversationFrom(projectID, location, modelName, pdfurl string) (string, error) {
	ctx := context.Background()

	// create a new generative AI client
	client, err := genai.NewClient(ctx, projectID, location)
	if err != nil {
		return "", fmt.Errorf("unable to create client: %w", err)
	}
	defer client.Close()

	// set the model name
	model := client.GenerativeModel(modelName)

	// create PDF part
	part := genai.FileData{
		MIMEType: "application/pdf",
		FileURI:  pdfurl,
	}

	// create prompt part
	tmpl := template.Must(
		template.New("podcast.tpl").ParseFS(promptTemplates, "prompts/podcast.tpl"),
	)
	buf := new(bytes.Buffer)
	err = tmpl.Execute(buf, nil)

	// Generate content
	res, err := model.GenerateContent(
		ctx,
		part,
		genai.Text(`"\n\n"`),
		genai.Text(buf.String()),
		/* 		genai.Text(`
		   		You are a very professional document summarization specialist.
		   		Please summarize the given document.
		   `), */
	)
	if err != nil {
		return "", fmt.Errorf("unable to generate contents: %w", err)
	}

	if len(res.Candidates) == 0 ||
		len(res.Candidates[0].Content.Parts) == 0 {
		return "", errors.New("empty response from model")
	}

	return fmt.Sprintf("%s", res.Candidates[0].Content.Parts[0]), nil
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
