# Cleanup models and providers

The goal of this workstream is to get rid of unnecessery providers and old models that are not needed anymore and focus only on a few vendors.

## Step 1
Remove following providers from provider and model packages and places where they referenced, ensure to remove code referencing these providers in config:

- copilot
- azure
- xai
- openrouter
- groq

## Step 2
Remove mentioning of these providers and their model ids from documentation (e.g. README.md); and from schema files and schema cmd tool.

## Step 3
Remove deprecated models from remaining providers, from docs and schema, here's the list of models that should remain, other removed:
- vertexai
	VertexAIGemini30Flash ModelID = "vertexai.gemini-3.0-flash"
	VertexAIGemini30Pro   ModelID = "vertexai.gemini-3.0-pro"
	VertexAISonnet45M     ModelID = "vertexai.claude-sonnet-4-5-m"
	VertexAIOpus45        ModelID = "vertexai.claude-opus-4-5"
- openai
	O3           ModelID = "o3"
	O4Mini       ModelID = "o4-mini"
	GPT5         ModelID = "gpt-5"
- gemini
	Gemini30Pro       ModelID = "gemini-3.0-pro"
	Gemini30Flash     ModelID = "gemini-3.0-flash"
- anthropic
	Claude45Sonnet1M ModelID = "claude-4-5-sonnet[1m]"
	Claude45Opus     ModelID = "claude-4.5-opus"
