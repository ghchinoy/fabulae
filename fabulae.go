// Copyright 2022 Google LLC
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

package fabulae

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	texttospeech "cloud.google.com/go/texttospeech/apiv1"
	"github.com/go-audio/wav"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/encoding/protojson"

	ttspb "cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

var striptags string

const timeformat = "20060102.030405.06"

func Speak(voice1name string, text string, gcsbucket string) (string, error) {
	outputfilename := fmt.Sprintf("%s.wav", time.Now().Format(timeformat))
	//voices := voice(voice1name)
	voices := getSpeechVoicesForName([]string{voice1name})

	log.Printf("Using: %s", jsonify(voices[voice1name]))
	log.Printf("text length: %d", len(text))
	log.Printf("output: %s", outputfilename)
	log.Printf("synthesizing ...")

	// generate audio
	ctx := context.Background()

	client, err := texttospeech.NewClient(ctx)
	if err != nil {
		return outputfilename, err
	}
	defer client.Close()

	//var input ttspb.SynthesisInput
	input := ttspb.SynthesisInput{
		InputSource: &ttspb.SynthesisInput_Text{Text: text},
	}
	//log.Printf("%s", string(ssml))
	if len(string(text)) > 5000 {
		return "", fmt.Errorf("too many characters: %d", len(text))
	}

	voice := voices[voice1name]
	req := ttspb.SynthesizeSpeechRequest{
		Input: &input,
		Voice: &voice,
		AudioConfig: &ttspb.AudioConfig{
			AudioEncoding: ttspb.AudioEncoding_LINEAR16,
		},
	}
	resp, err := client.SynthesizeSpeech(ctx, &req)
	if err != nil {
		return "", err
	}
	audiobytes := resp.AudioContent

	// write audio to output file and report
	err = os.WriteFile(outputfilename, audiobytes, 0644)
	if err != nil {
		log.Printf("unable to write to %s: %v", outputfilename, err)
		os.Exit(1)
	}
	log.Printf("Written %d bytes", len(audiobytes))
	fmt.Fprintf(os.Stdout, "Audio content written to file: %v\n", outputfilename)

	// report
	f, err := os.Open(outputfilename)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	dur, err := wav.NewDecoder(f).Duration()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s duration: %s\n", f.Name(), dur)
	return outputfilename, nil
}

type turnconfig struct {
	ID             int
	Turn           string
	Voice          ttspb.VoiceSelectionParams
	OutputFilename string
}

func Fabulae(voice1name, voice2name string, conversation string, outputfilename string, turnbyturn bool, tags string) ([]string, error) {
	striptags = tags

	if outputfilename == "" {
		outputfilename = fmt.Sprintf("%s.wav", time.Now().Format(timeformat))
	}

	// create turns from conversation string
	turns := strings.Split(conversation, "\n")

	// create SSML from conversation
	voices := getSpeechVoicesForName([]string{voice1name, voice2name})

	ctx := context.Background()

	outputfiles := []string{}

	if turnbyturn {
		log.Print("turn-by-turn requested")
		// remove blank lines
		cleanturns := []string{}
		for _, turn := range turns {
			if turn == "" {
				continue
			} else {
				turn = strings.Replace(turn, "| [*]", "", 1)
				turn = strings.Replace(turn, "| [+]\"", "", 1)
			}
			cleanturns = append(cleanturns, turn)
		}

		// goroutines

		// Configure turns
		configuredTurns := []turnconfig{}
		for i, turn := range cleanturns {
			var voice ttspb.VoiceSelectionParams
			if i%2 == 0 {
				voice = voices[voice1name]
			} else {
				voice = voices[voice2name]
			}
			turn = stripParticipantTags(turn, tags)
			configuredTurns = append(configuredTurns, turnconfig{
				ID:             i,
				Voice:          voice,
				Turn:           turn,
				OutputFilename: outputfilename,
			})
		}
		log.Printf("turns configured: %d", len(configuredTurns))

		outputfiles = processAudioTurns(configuredTurns)
		sort.Sort(sort.StringSlice(outputfiles))
		log.Printf("files: %s", outputfiles)

		/*
			// go routines for audio generation
			var wg sync.WaitGroup
			outputfileChannel := processAudio(configuredTurns...)
			wg.Add(1)

			go func() {
				for val := range outputfileChannel {
					log.Printf("channel1 data: %v", val)
					if val != "" {
						outputfiles = append(outputfiles, val)
					}
				}
				wg.Done()
			}()

			wg.Wait()
			log.Printf("done! %v", outputfiles)
		*/

		/*
			// serially
			for i, turn := range cleanturns {
				var voice ttspb.VoiceSelectionParams
				if i%2 == 0 {
					voice = voices[voice1name]
				} else {
					voice = voices[voice2name]
				}
				turn = stripParticipantTags(turn, tags)
				log.Printf("voice: %s", voice.Name)
				//log.Printf("turn: %s")
				audiobytes, err := synthesizeWithVoice(ctx, voice, turn)
				if err != nil {
					log.Printf("error in synthesis for %d: %v", i, err)
					return outputfiles, err
				}
				dir, filename := filepath.Split(outputfilename)
				filename = fmt.Sprintf("%02d_%s", i, filename)

				turnfilename := filepath.Join(dir, filename)
				err = os.WriteFile(turnfilename, audiobytes, 0644)
				if err != nil {
					log.Printf("unable to write to %s: %v", turnfilename, err)
					return outputfiles, err
				}
				log.Printf("Audio content written to file (%d bytes): %v", len(audiobytes), turnfilename)
				//fmt.Fprintf(os.Stderr, "Audio content (%d bytes) written to file: %v\n", len(audiobytes), turnfilename)
				outputfiles = append(outputfiles, turnfilename)
			}
		*/

	} else {
		ssml := generateSSMLfromConversation(turns, []ttspb.VoiceSelectionParams{voices[voice1name], voices[voice2name]})
		//log.Print(ssml)

		// generate audio

		audiobytes, err := synthesize(ctx, ssml)
		if err != nil {
			log.Printf("error in synthesis: %v", err)
			os.Exit(1)
		}

		// write audio to output file and report
		err = os.WriteFile(outputfilename, audiobytes, 0644)
		if err != nil {
			log.Printf("unable to write to %s: %v", outputfilename, err)
			os.Exit(1)
		}
		log.Printf("Written %d bytes", len(audiobytes))
		fmt.Fprintf(os.Stdout, "Audio content written to file: %v\n", outputfilename)

		// report
		f, err := os.Open(outputfilename)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		dur, err := wav.NewDecoder(f).Duration()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s duration: %s\n", f.Name(), dur)
		outputfiles = append(outputfiles, outputfilename)
	}

	return outputfiles, nil

}

func processAudioTurns(turns []turnconfig) []string {
	ctx := context.Background()

	var wg sync.WaitGroup
	results := []string{}
	resultChan := make(chan string, len(turns))

	for i, turn := range turns {
		wg.Add(1)
		go func(i int, turn turnconfig) {
			defer wg.Done()
			//log.Printf("goroutine: %d; turn %d; voice: %s", i, turn.ID, turn.Voice.Name)
			audiobytes, err := synthesizeWithVoice(ctx, turn.Voice, turn.Turn)
			if err != nil {
				resultChan <- fmt.Sprintf("error goroutine: %d; turn %d; voice: %s", i, turn.ID, turn.Voice.Name)
			}

			dir, filename := filepath.Split(turn.OutputFilename)
			filename = fmt.Sprintf("%02d_%s", turn.ID, filename)

			turnfilename := filepath.Join(dir, filename)
			err = os.WriteFile(turnfilename, audiobytes, 0644)

			if err != nil {
				resultChan <- fmt.Sprintf("unable to write to %s: %v", turnfilename, err)
			}
			log.Printf("Audio content written to file (%d bytes): %v", len(audiobytes), turnfilename)
			resultChan <- turnfilename
		}(i, turn)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for r := range resultChan {
		results = append(results, r)
	}

	return results
}

// synthesizeWithVoice takes a string and a voice and returns audio bytes using GCP TTS
func synthesizeWithVoice(ctx context.Context, voice ttspb.VoiceSelectionParams, turn string) ([]byte, error) {
	//log.Printf("voice: %s", voice.Name)
	opts := []option.ClientOption{}
	if strings.Contains(voice.Name, "Neural") {
		opts = append(opts, option.WithEndpoint("texttospeech.googleapis.com:443"))
	}
	client, err := texttospeech.NewClient(ctx, opts...)
	if err != nil {
		return []byte{}, err
	}
	defer client.Close()

	req := ttspb.SynthesizeSpeechRequest{
		Input: &ttspb.SynthesisInput{
			InputSource: &ttspb.SynthesisInput_Text{Text: turn},
		},
		Voice: &voice,
		AudioConfig: &ttspb.AudioConfig{
			AudioEncoding: ttspb.AudioEncoding_LINEAR16,
		},
	}
	resp, err := client.SynthesizeSpeech(ctx, &req)
	if err != nil {
		return []byte{}, err
	}
	return resp.AudioContent, nil
}

// synthesize takes a block of SSML and generates audio bytes using GCP TTS
func synthesize(ctx context.Context, ssml string) ([]byte, error) {
	// note use of us-central1 endpoint for Neural2 voices
	client, err := texttospeech.NewClient(
		ctx,
		//option.WithEndpoint("texttospeech.googleapis.com:443"),
	)
	if err != nil {
		return []byte{}, err
	}
	defer client.Close()

	//var input ttspb.SynthesisInput
	input := ttspb.SynthesisInput{
		InputSource: &ttspb.SynthesisInput_Ssml{Ssml: string(ssml)},
	}
	//log.Printf("%s", string(ssml))
	if len(string(ssml)) > 5000 {
		return []byte{}, fmt.Errorf("too many characters: %d", len(string(ssml)))
	}

	req := ttspb.SynthesizeSpeechRequest{
		Input: &input,
		Voice: &ttspb.VoiceSelectionParams{
			LanguageCode: "en-US",
		},
		AudioConfig: &ttspb.AudioConfig{
			AudioEncoding: ttspb.AudioEncoding_LINEAR16,
		},
	}
	log.Printf("%v", req)
	resp, err := client.SynthesizeSpeech(ctx, &req)
	if err != nil {
		log.Printf("error in SynthesizeSpeech: %v", err)
		return []byte{}, err
	}
	return resp.AudioContent, nil
}

// generateSSMLfromConversation takes a turn-by-turn 2 person conversation, one turn per line
// and turns it into a <speak>...</speak> ssml string
func generateSSMLfromConversation(turns []string, voices []ttspb.VoiceSelectionParams) string {
	ssml := []string{}
	ssml = append(ssml, "<speak>")

	for k, v := range turns {
		v := stripParticipantTags(v, striptags)
		ssml = append(ssml, fmt.Sprintf("<mark name=\"%d\"/><voice name=\"%s\">%s</voice>", k, voices[k%2].Name, v))
		ssml = append(ssml, "<break time=\"250ms\"/>")
	}
	ssml = append(ssml, "</speak>")
	return strings.Join(ssml, "")
}

func stripParticipantTags(text string, striptags string) string {
	if len(striptags) == 0 {
		return text
	}
	strip := strings.Split(striptags, ",")
	for _, s := range strip {
		if !strings.HasSuffix(s, ":") {
			strip = append(strip, fmt.Sprintf("%s:", s))
		}
	}
	for _, s := range strip {
		text = strings.Replace(text, s, "", 1)
	}

	return text
}

func getSpeechVoicesForName(voicenames []string) map[string]ttspb.VoiceSelectionParams {
	voices, _ := listVoices()

	response := make(map[string]ttspb.VoiceSelectionParams, len(voicenames))

	for _, name := range voicenames {
		for _, v := range voices {
			if v.Name == name {
				log.Printf("found %s: %v", name, v)
				voice := ttspb.VoiceSelectionParams{
					Name:         v.Name,
					SsmlGender:   v.SsmlGender,
					LanguageCode: "en-US",
				}
				response[name] = voice
				continue
			}
		}
	}

	return response
}

func listVoices() ([]*ttspb.Voice, error) {
	ctx := context.Background()
	client, err := texttospeech.NewClient(
		ctx,
		//option.WithEndpoint("texttospeech.googleapis.com:443"),
	)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	listRequest := &ttspb.ListVoicesRequest{}
	voicesResponse, err := client.ListVoices(ctx, listRequest)
	if err != nil {
		return nil, err
	}

	return voicesResponse.Voices, nil
}

// jsonify prints nicely
func jsonify(voice ttspb.VoiceSelectionParams) string {
	encoder := protojson.MarshalOptions{
		Indent: " ",
	}
	voicebytes, err := encoder.Marshal(&voice)
	if err != nil {
		return fmt.Sprintf("%+v", voice)
	}
	return string(voicebytes)
}
