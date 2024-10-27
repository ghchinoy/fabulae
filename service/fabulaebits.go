package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"cloud.google.com/go/vertexai/genai"
)

var (
	modelName   = "gemini-1.5-pro"
	sourcesPath = "sources"
	audioPath   = "audio"
)

//go:embed prompts/*.tpl
var promptTemplates embed.FS // Embed prompt templates from the prompts directory

// addPDFSourceToGCS adds the PDF to GCS source bucket
func addPDFSourceToGCS(httpurl string) (string, error) {
	// get and check mime type
	response, err := http.Get(httpurl)
	if err != nil {
		log.Printf("unable to http.Get: %v", err)
	}
	contentType := response.Header.Get("Content-Type")
	log.Printf("mime-type: %s", contentType)
	if !strings.Contains(contentType, "application/pdf") {
		return "", fmt.Errorf("Sorry this doesn't appear to be a PDF: %s", httpurl)
	}

	// get and add to gcs
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("apologies, I couldn't download %s: %v", httpurl, err)
	}
	u, _ := url.Parse(httpurl)
	path := u.Path
	resourceName := path[strings.LastIndex(path, "/")+1:] + ".pdf"

	log.Printf("this is what I've chosen for the filename: %s", resourceName)
	gcsurl, err := storeBytesToBucket(body, resourceName)
	if err != nil {
		log.Printf("error storeBytesToBucket: %v", err)
		return "", fmt.Errorf("apologies, I couldn't save %s: %v", httpurl, err)

	}
	return gcsurl, nil
}

// getTitleOfDocument uses Gemini Controlled Generation to output a title
func getTitleOfDocument(pdfurl string) string {

	//ctx := context.Background()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(time.Second*120))
	defer cancel()

	// create a new generative AI client
	client, err := genai.NewClient(ctx, projectID, location)
	if err != nil {
		log.Printf("unable to create client: %v", err)
		return ""
	}
	defer client.Close()

	model := client.GenerativeModel("gemini-1.5-flash")
	model.ResponseMIMEType = "application/json"
	model.SafetySettings = []*genai.SafetySetting{
		{
			Category:  genai.HarmCategoryHarassment,
			Threshold: genai.HarmBlockOnlyHigh,
		},
		{
			Category:  genai.HarmCategoryDangerousContent,
			Threshold: genai.HarmBlockOnlyHigh,
		},
	}

	// create PDF part
	documentPart := genai.FileData{
		MIMEType: "application/pdf",
		FileURI:  pdfurl,
	}

	parts := []genai.Part{
		documentPart,
		genai.Text(`extract the title only from this document, if there isn't a title, provide a short few word title. Make sure it's in this form only:
{"title": "title of document"}`)}

	res, err := model.GenerateContent(ctx, parts...)
	if err != nil {
		log.Printf("unable to generate title contents: %v", err)
		return ""
	}
	var doc DocumentInfo
	err = json.Unmarshal([]byte(fmt.Sprintf("%s", res.Candidates[0].Content.Parts[0])), &doc)
	if err != nil {
		log.Printf("couldn't unmarshal: %s: %v", res.Candidates[0].Content.Parts[0], err)
		return ""
	}

	return doc.Title
}

type DocumentInfo struct {
	Title string `json:"title"`
}

func removeNonAlphanumerics(input string) string {
	input = strings.ReplaceAll(input, " ", "")

	// Remove all non-alphanumeric characters
	input = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, input)
	return input
}

// createConversationFromPDFURL generates a conversation from a PDF URL using a generative AI model
func createConversationFromPDFURL(pdfurl string) (string, error) {
	log.Printf("generating conversation from %s ...", pdfurl)
	conversation, err := generateConversationFrom(projectID, location, modelName, pdfurl)
	if err != nil {
		return "", err
	}
	log.Print("conversation created")
	return conversation, nil
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

	model.SafetySettings = []*genai.SafetySetting{
		{
			Category:  genai.HarmCategoryHarassment,
			Threshold: genai.HarmBlockOnlyHigh,
		},
		{
			Category:  genai.HarmCategoryDangerousContent,
			Threshold: genai.HarmBlockOnlyHigh,
		},
	}

	// create PDF part
	part := genai.FileData{
		MIMEType: "application/pdf",
		FileURI:  pdfurl,
	}

	// create prompt part
	var prompt string

	// use built-in prompt
	if prompt == "" {
		tmpl := template.Must(
			template.New("podcast.tpl").ParseFS(promptTemplates, "prompts/podcast.tpl"),
		)
		buf := new(bytes.Buffer)
		err = tmpl.Execute(buf, nil)
		prompt = buf.String()
	}

	// parts for both token count and generation
	parts := []genai.Part{
		part,
		genai.Text(`"\n\n"`),
		genai.Text(prompt),
	}

	// count tokens
	if tr, err := model.CountTokens(ctx, parts...); err == nil {
		log.Printf("processing %s tokens ...", strconv.FormatInt(int64(tr.TotalTokens), 10))
	}

	// generate content
	res, err := model.GenerateContent(ctx, parts...)
	if err != nil {
		return "", fmt.Errorf("unable to generate contents: %w", err)
	}

	if len(res.Candidates) == 0 ||
		len(res.Candidates[0].Content.Parts) == 0 {
		return "", errors.New("empty response from model")
	}

	return fmt.Sprintf("%s", res.Candidates[0].Content.Parts[0]), nil
}

/*
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
*/

func storeBytesToBucket(pdffile []byte, filename string) (string, error) {
	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	parts := strings.Split(audioBucketPath, "/")
	bucketName := parts[0]
	//storagePath := strings.Join(parts[1:], "/")
	storagePath := sourcesPath

	objectName := fmt.Sprintf("%s/%s", storagePath, filename)
	gcsurl := fmt.Sprintf("gs://%s/%s", bucketName, objectName)

	log.Printf("writing to %s %s as %s", bucketName, objectName, gcsurl)
	o := client.Bucket(bucketName).Object(objectName)

	//o = o.If(storage.Conditions{DoesNotExist: true})

	wc := o.NewWriter(ctx)
	f := bytes.NewReader(pdffile)
	if _, err = io.Copy(wc, f); err != nil {
		return gcsurl, fmt.Errorf("io.Copy: %w", err)
	}
	if err := wc.Close(); err != nil {
		return gcsurl, fmt.Errorf("Writer.Close: %w", err)
	}

	log.Printf("written %d bytes to %s", len(pdffile), gcsurl)
	return gcsurl, nil
}

// moveFilesToAudioBucket moves files to a GCS bucket
func moveFilesToAudioBucket(outputfiles []string) error {
	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	parts := strings.Split(audioBucketPath, "/")
	bucketName := parts[0]
	//storagePath := strings.Join(parts[1:], "/")
	storagePath := audioPath

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
