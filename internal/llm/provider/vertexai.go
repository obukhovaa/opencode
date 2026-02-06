package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"cloud.google.com/go/auth"
	sdkoption "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/vertex"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/logging"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"google.golang.org/genai"
)

type VertexAIClient ProviderClient

type vertexOptions struct {
	projectID           string
	location            string
	locationForCounting string
}

func newVertexAIClient(opts providerClientOptions) VertexAIClient {
	for k := range models.VertexAIAnthropicModels {
		if k == opts.model.ID {
			logging.Info("Using Anthropic client with VertexAI provider", "model", k)
			location := os.Getenv("VERTEXAI_LOCATION")
			locationForCounting := os.Getenv("VERTEXAI_LOCATION_COUNT")
			if len(locationForCounting) == 0 {
				// NOTE: there's no counting endpoint on global for anthropic models
				if location == "global" {
					locationForCounting = "us-east5"
				} else {
					locationForCounting = location
				}
			}
			projectID := os.Getenv("VERTEXAI_PROJECT")
			opts.anthropicOptions = append(opts.anthropicOptions,
				WithVertexAI(projectID, location, locationForCounting),
			)
			return newAnthropicClient(opts)
		}
	}

	geminiOpts := geminiOptions{}
	for _, o := range opts.geminiOptions {
		o(&geminiOpts)
	}
	genaiConfig := &genai.ClientConfig{
		Project:  os.Getenv("VERTEXAI_PROJECT"),
		Location: os.Getenv("VERTEXAI_LOCATION"),
		Backend:  genai.BackendVertexAI,
	}

	if opts.baseURL != "" {
		if opts.headers != nil {
			header := opts.asHeader()
			genaiConfig.HTTPOptions = genai.HTTPOptions{
				BaseURL: opts.baseURL,
				Headers: *header,
			}
		} else {
			genaiConfig.HTTPOptions = genai.HTTPOptions{
				BaseURL: opts.baseURL,
			}
		}
	}

	if opts.apiKey != "" {
		genaiConfig.Credentials = &auth.Credentials{
			TokenProvider: &tokenProvider{value: opts.apiKey},
		}
	}

	client, err := genai.NewClient(context.Background(), genaiConfig)
	if err != nil {
		logging.Error("Failed to create VertexAI client", "error", err)
		return nil
	}

	logging.Info("Using Gemini client with VertexAI provider", "model", opts.model.ID, "config", client.ClientConfig())
	return &geminiClient{
		providerOptions: opts,
		options:         geminiOpts,
		client:          client,
	}
}

// NOTE: copied from (here)[github.com/anthropics/anthropic-sdk-go/vertex] to make LiteLLM passthrough work
func vertexMiddleware(region, regionForCounting, projectID string) sdkoption.Middleware {
	return func(r *http.Request, next sdkoption.MiddlewareNext) (*http.Response, error) {
		if r.Body != nil {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			r.Body.Close()

			if !gjson.GetBytes(body, "anthropic_version").Exists() {
				body, _ = sjson.SetBytes(body, "anthropic_version", vertex.DefaultVersion)
			}

			model := gjson.GetBytes(body, "model").String()
			stream := gjson.GetBytes(body, "stream").Bool()
			betas := r.Header.Values("anthropic-beta")
			newPath := ""

			if strings.HasSuffix(r.URL.Path, "/v1/messages") && r.Method == http.MethodPost {
				if len(betas) > 0 {
					body, _ = sjson.SetBytes(body, "anthropic_beta", betas)
				}
				if projectID == "" {
					return nil, fmt.Errorf("no projectId was given and it could not be resolved from credentials")
				}

				// HACK: vertex expect no model in body here
				body, _ = sjson.DeleteBytes(body, "model")

				specifier := "rawPredict"
				if stream {
					specifier = "streamRawPredict"
				}
				newPath = fmt.Sprintf("/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:%s", projectID, region, model, specifier)
				r.URL.Path = strings.ReplaceAll(r.URL.Path, "/v1/messages", newPath)
			}

			if strings.HasSuffix(r.URL.Path, "/v1/messages/count_tokens") && r.Method == http.MethodPost {
				if len(betas) > 0 {
					body, _ = sjson.SetBytes(body, "anthropic_beta", betas)
				}
				if projectID == "" {
					return nil, fmt.Errorf("no projectId was given and it could not be resolved from credentials")
				}

				// HACK: vertex expect no beta in header
				r.Header.Del("anthropic-beta")

				newPath = fmt.Sprintf("/v1/projects/%s/locations/%s/publishers/anthropic/models/count-tokens:rawPredict", projectID, regionForCounting)
				r.URL.Path = strings.ReplaceAll(r.URL.Path, "/v1/messages/count_tokens", newPath)
			}

			logging.Debug("vertext_ai middleware request, using beta header", "anthropic-beta", betas,
				"model", model, "stream", stream, "path", r.URL.Path, "new_path", newPath, "method", r.Method, "body", string(body),
			)

			reader := bytes.NewReader(body)
			r.Body = io.NopCloser(reader)
			r.GetBody = func() (io.ReadCloser, error) {
				_, err := reader.Seek(0, 0)
				return io.NopCloser(reader), err
			}
			r.ContentLength = int64(len(body))
		}

		return next(r)
	}
}
